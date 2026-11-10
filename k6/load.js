// Нагрузка на все сервисы демо.
// TARGET_PATH, DURATION, RPS, RPS_GOROUTINE, RPS_CLIENT, RPS_CTX, RPS_DB приходят из compose.
import exec from 'k6/execution';
import http from 'k6/http';
import { check, sleep } from 'k6';

const path = (__ENV.TARGET_PATH || '/leak').startsWith('/')
  ? __ENV.TARGET_PATH || '/leak'
  : `/${__ENV.TARGET_PATH}`;
const duration = __ENV.DURATION || '5m';

const services = [
  { name: 'body', base: __ENV.URL_BODY || 'http://http_leak:8080', rps: Number(__ENV.RPS || 50) },
  { name: 'goroutine', base: __ENV.URL_GOROUTINE || 'http://goroutine_leak:8080', rps: Number(__ENV.RPS_GOROUTINE || 50) },
  { name: 'client', base: __ENV.URL_CLIENT || 'http://client_leak:8080', rps: Number(__ENV.RPS_CLIENT || 50) },
  { name: 'ctx', base: __ENV.URL_CTX || 'http://ctx_leak:8080', rps: Number(__ENV.RPS_CTX || 50) },
  { name: 'db', base: __ENV.URL_DB || 'http://db_leak:8080', rps: Number(__ENV.RPS_DB || 50) },
];

const urls = {};
const scenarios = {};
for (const svc of services) {
  if (!Number.isFinite(svc.rps) || svc.rps <= 0) {
    throw new Error(`RPS для ${svc.name} должен быть положительным числом, получено: ${svc.rps}`);
  }
  urls[svc.name] = `${svc.base}${path}`;
  scenarios[svc.name] = {
    executor: 'constant-arrival-rate',
    rate: svc.rps,
    timeUnit: '1s',
    duration,
    preAllocatedVUs: 10,
    maxVUs: 50,
    exec: 'hit',
  };
}

export const options = {
  scenarios,
  discardResponseBodies: true,
};

export function setup() {
  for (const svc of services) {
    const probe = `${svc.base}/`;
    let ready = false;
    for (let i = 0; i < 30; i++) {
      const res = http.get(probe, { timeout: '1s' });
      if (res.status === 200) {
        ready = true;
        break;
      }
      sleep(0.5);
    }
    if (!ready) {
      exec.test.abort(`сервис не ответил на ${probe}`);
    }
    console.log(`${svc.name} ${urls[svc.name]}  rps=${svc.rps}  duration=${duration}`);
  }
}

export function hit() {
  const res = http.get(urls[exec.scenario.name], { timeout: '3s' });
  check(res, {
    'status 200': (r) => r.status === 200,
  });
}
