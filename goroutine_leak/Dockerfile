FROM golang:1.23.1-alpine AS build

WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 go build -o /service .

FROM alpine:3.20

RUN apk add --no-cache wget \
    && adduser -D -H -u 65532 app
COPY --from=build /service /service
USER app

EXPOSE 8080
ENTRYPOINT ["/service"]
CMD ["-http", "0.0.0.0:8080"]
