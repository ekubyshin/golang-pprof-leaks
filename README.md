# Профилирование Go-сервисов: pprof

Материалы вебинара: разбор инцидента с утечкой памяти и живое демо той же ошибки в `http_leak`.

Презентация: [`slides.md`](slides.md) (Marp) и [`slides.pdf`](slides.pdf).
Скрины инцидента лежат в [`screens/`](screens).

Все команды ниже запускаются из корня этого каталога.

## Что поднимается

`docker compose up` собирает семь контейнеров: шесть сервисов и k6. У каждого сервиса свой Dockerfile в его папке. k6 стартует, когда все сервисы отвечают на `GET /`, и бьёт в один и тот же путь на каждом. По умолчанию это `/leak`.

| Контейнер | Порт на хосте | Что течёт на `/leak` | RPS по умолчанию |
| --- | --- | --- | --- |
| `http_leak` | `127.0.0.1:8080` | Незакрытый `resp.Body` | 50 |
| `goroutine_leak` | `127.0.0.1:8091` | Горутина на запрос блокируется на канале | 20 |
| `client_leak` | `127.0.0.1:8092` | Новый `http.Transport` на каждый запрос | 20 |
| `ctx_leak` | `127.0.0.1:8093` | `cancel` не вызван, горутина ждёт `ctx.Done()` | 20 |
| `db_leak` | `127.0.0.1:8094` | `sql.Rows` не закрыты, соединение остаётся занятым | 10 |
| `tx_leak` | `127.0.0.1:8095` | Транзакция без `Commit` и `Rollback`, соединение и `awaitDone` остаются | 10 |
| `k6` | нет | Нагрузка, [`k6/load.js`](k6/load.js) | |

Ручки у всех сервисов одинаковые:

| URL | Поведение |
| --- | --- |
| `GET /leak` | Воспроизводит утечку этого сервиса |
| `GET /fixed` | Тот же вызов без утечки |
| `GET /gc` | Сборка мусора и текущие счётчики |
| `GET /debug/pprof/` | Профили процесса |

Проверка здоровья ходит в `/` и сама соединения не копит. k6 закрывает тела ответов сервисов.

В `http_leak` клиент один на процесс, downstream в том же процессе. Разница `/leak` и `/fixed` только в `Close`. В профиле горутин видны `readLoop`, `writeLoop` и `conn.serve`.

В `client_leak` на `/leak` у каждого запроса свой нулевой `Transport`: `IdleConnTimeout` равен 0, простаивающее соединение остаётся. `/fixed` пользуется одним клиентом с таймаутом простоя 90 секунд. Body закрывается в обоих случаях.

В `goroutine_leak` на запрос запускается горутина, которая пишет в канал без читателя и держит буфер 4 КБ.

В `ctx_leak` родительский контекст живёт до конца процесса. На `/leak` дочерний `cancel` не вызывается, горутина блокируется на `ctx.Done()`.

В `db_leak` база встроенная: SQLite в памяти, пакет `modernc.org/sqlite`, без CGO и без отдельного контейнера. На `/leak` `Rows` не закрываются. Соединение занято, пока не вызван `Rows.Close`: сборщик сам его не вызывает, срез держит `Rows` достижимым. В логе растут `rows` и `conns_in_use`, число горутин почти стоит. `/fixed` делает `defer rows.Close()` и дочитывает выборку.

В `tx_leak` та же SQLite в памяти. На `/leak` `BeginTx(context.Background())` читает одну строку и не вызывает `Commit` или `Rollback`. `database/sql` держит соединение и горутину `(*Tx).awaitDone`, пока транзакция не закрыта. В логе растут `tx`, `conns_in_use` и число горутин, `heap_inuse` почти стоит. `/fixed` делает `defer tx.Rollback()` и `Commit`. Чтение берёт shared-блокировку и не мешает другим читателям. Незакоммиченная запись остановила бы писателя.

| Переменная | По умолчанию | Смысл |
| --- | --- | --- |
| `TARGET_PATH` | `/leak` | Путь на всех сервисах |
| `DURATION` | `2m` | Длительность k6 |
| `RPS` | `50` | RPS в `http_leak` |
| `RPS_GOROUTINE` | `20` | RPS в `goroutine_leak` |
| `RPS_CLIENT` | `20` | RPS в `client_leak` |
| `RPS_CTX` | `20` | RPS в `ctx_leak` |
| `RPS_DB` | `10` | RPS в `db_leak` |
| `RPS_TX` | `10` | RPS в `tx_leak` |

## Что понадобится

- Docker Compose. Образ k6 скачивается при первом старте.
- Go на хосте, чтобы смотреть снятые профили: `go tool pprof`. Собирать сервис локально не нужно.
- Для режима Graph в pprof: [Graphviz](https://graphviz.org), на macOS это `brew install graphviz`. Без `dot` работают Top, Flame Graph и Source.
- Чтобы заново собрать PDF: Node.js и `npx`. Команда в конце файла.

Порты `127.0.0.1:8080`, `8091`, `8092`, `8093`, `8094` и `8095` должны быть свободны.

## Демонстрация

Нужны два терминала: в одном лог сервиса, во втором профили. k6 работает сам.

### 1. Сначала здоровый путь

```bash
TARGET_PATH=/fixed DURATION=30s docker compose up --build -d
docker compose logs -f http_leak
```

k6 две-три секунды ждёт healthy у всех сервисов и затем 30 секунд держит нагрузку на `/fixed`: 50 RPS в `http_leak`, по 20 RPS в горутины, клиент и контекст, по 10 RPS в базу и транзакции. В логе каждого сервиса горутины и соединения остаются на полке.

Строка раз в две секунды:

```text
goroutines=   10 (   +7)  heap_inuse=   1MiB  stack=  640KiB  sys=  17MiB  gc=2
```

- `(+N)` — насколько выросло число горутин с момента старта процесса
- `heap_inuse` — память кучи, которую процесс ещё держит
- `stack` — память стеков горутин
- `gc` — сколько сборок мусора уже было

Принудительный GC с хоста:

```bash
curl -fsS http://127.0.0.1:8080/gc
```

После GC счётчик горутин остаётся на той же полке. Отчёт k6: `docker compose logs k6`. Лог сервиса можно отпустить через Ctrl+C: это останавливает только `logs -f`, контейнеры продолжают работать.

При старте сервис пишет `heap sample rate 65536 bytes`. В демо куча сэмплируется каждые 64 КБ, чтобы короткий прогон показал буферы `net/http`. На проде значение по умолчанию — 512 КБ.

### 2. Включить утечку

Контейнер перед этим уже походил в `/fixed`, но старые соединения в нём не остаются: `/fixed` их возвращает в пул. Для чистого графика всё равно поднимите новый процесс. Без `--build`, образ уже собран.

```bash
docker compose down
docker compose up -d
docker compose logs -f http_leak
```

Теперь k6 две минуты бьёт в `/leak`: 50 RPS в `http_leak`, по 20 RPS в горутины, клиент и контекст, по 10 RPS в базу и транзакции. Для `http_leak` ожидание прежнее: около **+150 горутин в секунду**, то есть **+300** на каждую двухсекундную строку. На запрос остаются три горутины:

- `net/http.(*persistConn).readLoop`
- `net/http.(*persistConn).writeLoop`
- `net/http.(*conn).serve` у downstream

Вместе с ними растут `heap_inuse` и `stack`. Прогон на 50 RPS выглядит примерно так:

```text
goroutines=    5 (   +2)  heap_inuse=   1MiB
goroutines=  400 ( +397)  heap_inuse=   6MiB
goroutines= 1600 (+1597)  heap_inuse=  18MiB
goroutines= 3305 (+3302)  heap_inuse=  28MiB
```

Когда k6 заканчивается, контейнер `http_leak` остаётся запущенным, и счётчик больше не растёт. `curl http://127.0.0.1:8080/gc` число горутин не уменьшает: стеки всё ещё живые.

Рост RSS всех контейнеров:

```bash
docker stats
```

Если в отчёте k6 проверки `status 200` пошли вниз, процесс уже упёрся в лимит дескрипторов или портов. Для сюжета этого достаточно.

### 3. Посмотреть дамп горутин

Пока k6 ещё стучит в `/leak` или сразу после него, пока контейнер жив:

```bash
curl -fsS 'http://127.0.0.1:8080/debug/pprof/goroutine?debug=1' | head -n 40
```

Группы отсортированы по убыванию. Сверху оказываются `readLoop`, `writeLoop` и `conn.serve` со счётчиком порядка числа уже сделанных запросов.

`debug=2` добавляет к кадрам аргументы. В них могут быть URL и заголовки, поэтому для демонстрации хватает `debug=1`.

### 4. Снять два профиля со всех сервисов и посчитать diff

Второй терминал, пока k6 ещё работает (окно нагрузки — две минуты). [`scripts/capture-profiles.sh`](scripts/capture-profiles.sh) обходит шесть сервисных контейнеров, пишет heap и goroutine на хост, через 60 секунд снимает те же профили ещё раз и для каждого файла считает `-base`. k6 в обход не входит.

```bash
./scripts/capture-profiles.sh
```

Другая пауза, в секундах: `./scripts/capture-profiles.sh 45`. Два снимка вручную, паузу между ними выбираете сами:

```bash
./scripts/capture-first.sh
./scripts/capture-second.sh
```

Файлы лежат в `profiles/<сервис>.heap1.pb.gz` и `profiles/<сервис>.goroutine2.pb.gz`: первая цифра — снимок, имя — контейнер.

Веб-интерфейс с `-diff_base` открывает [`scripts/pprof-diff.sh`](scripts/pprof-diff.sh): один `pprof` в переднем плане, как ручной запуск. Heap по умолчанию на `9999`. Процесс живёт, пока его не остановите.

```bash
./scripts/pprof-diff.sh http_leak
./scripts/pprof-diff.sh http_leak goroutine
```

Имена: `http_leak`, `goroutine_leak`, `client_leak`, `ctx_leak`, `db_leak`, `tx_leak`. Goroutine по умолчанию на `9998`. Другой порт: `./scripts/pprof-diff.sh client_leak heap 9992`. Если скрипт пишет «дельта нулевая», снимки совпали и график пустой: пара снята, когда счётчик в логе уже стоял.

Как читать результат:

- **`-base`** вычитает первый снимок из второго. В `-top` проценты считаются от дельты, колонка `cum` показывает, сколько стеков прибавилось. У `http_leak` за 30 секунд при 50 RPS это около полутора тысяч `readLoop`, `writeLoop` и `conn.serve`. У остальных сервисов дельта меньше и другая по составу: ориентиры в разделе ниже.
- **`-diff_base`** нужен веб-интерфейсу: Top, Graph, Flame Graph, Source. В текстовом `-top` проценты в этом режиме легко уезжают за 100%, потому что знаменатель — размер базового снимка. Ориентир — абсолютные числа.
- Оба флага вместе передать нельзя.
- В heap по **`inuse_space`** в приросте `http_leak` и `client_leak` видны `bufio.NewReaderSize`, `bufio.NewWriterSize` и `net/http.(*Transport).dialConn`.
- **`alloc_space`** за то же окно часто подсвечивает сам сбор профиля (`runtime/pprof`, `compress/flate`). Это аллокации, которые к следующему GC уже могут умереть. Утечку смотрим по `inuse_space` и по горутинам.
- В UI у heap есть переключатель sample: `inuse_space` для живых объектов, `alloc_space` для суммарных аллокаций. По умолчанию открывается `inuse_space`.
- Graph требует `dot`. Если его нет, оставайтесь в Top, Flame Graph и Source.

Прямые ссылки, если UI pprof не нужен:

- <http://127.0.0.1:8080/debug/pprof/>
- <http://127.0.0.1:8080/debug/pprof/heap>
- <http://127.0.0.1:8080/debug/pprof/goroutine>
- <http://127.0.0.1:8080/debug/pprof/allocs>
- <http://127.0.0.1:8080/debug/pprof/profile?seconds=20>

CPU-профиль на 20 секунд заметно грузит процесс. На этой утечке он почти пустой: горутины спят на сокете, а не крутят процессор.

### 5. Остановить

```bash
docker compose down
```

Файлы в `profiles/` можно удалить, они в `.gitignore`.

Другой RPS или длительность, снова с чистого контейнера:

```bash
docker compose down
RPS=100 DURATION=45s docker compose up -d
```

## Остальные сервисы в этом же запуске

k6 грузит их параллельно с `http_leak`. Лог и pprof у каждого свои.

```bash
docker compose logs -f goroutine_leak
docker compose logs -f client_leak
docker compose logs -f ctx_leak
```

На `/leak` при RPS по умолчанию:

- `goroutine_leak` и `ctx_leak` — около +40 горутин за две секунды, по одной на запрос. Стек стоит на отправке в канал или на `ctx.Done()`
- `client_leak` — около +120 за две секунды: `readLoop`, `writeLoop` и `conn.serve` downstream в том же процессе. Body при этом закрыт
- `db_leak` — около +20 `rows` и `conns_in_use` за две секунды. Горутины при этом почти не растут: соединение просто не возвращается в пул. В логе это поля `rows` и `conns_in_use`. После `GET /gc` они остаются
- `tx_leak` — около +20 `tx`, `conns_in_use` и горутин за две секунды. Стек стоит в `database/sql.(*Tx).awaitDone`. `heap_inuse` почти не растёт. После `GET /gc` счётчики остаются

Два снимка и текстовый `-base` по всем шести сервисам считает `./scripts/capture-profiles.sh`. Текстовый дамп горутин одного сервиса:

```bash
curl -fsS 'http://127.0.0.1:8091/debug/pprof/goroutine?debug=1' | head -n 20
```

Для клиента, контекста, базы и транзакций порты `8092`, `8093`, `8094` и `8095`. Те же пути, что у `http_leak`: `/debug/pprof/`, `heap`, `goroutine`, `allocs`.

В `db_leak` смотреть в первую очередь счётчик соединений, не профиль горутин. В `tx_leak` рядом с ним виден `awaitDone`:

```bash
docker compose logs -f db_leak
curl -fsS http://127.0.0.1:8094/gc
docker compose logs -f tx_leak
curl -fsS http://127.0.0.1:8095/gc
```



## Собрать PDF

```bash
npx --yes @marp-team/marp-cli slides.md --pdf --allow-local-files -o slides.pdf
```

Превью в браузере:

```bash
npx --yes @marp-team/marp-cli slides.md --html --allow-local-files -o slides.html
```

`--allow-local-files` нужен, чтобы в слайды попали картинки из `screens/`.
