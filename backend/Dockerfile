FROM golang:1.23-alpine AS build

WORKDIR /app

COPY . ./
RUN if [ ! -f go.mod ]; then go mod init gomoku-backend; fi \
    && go mod tidy \
    && go build -o server .

FROM alpine:3.20

ARG LOCAL_UID=1000
ARG LOCAL_GID=1000

RUN mkdir -p /app /cache_logs \
    && chown -R "${LOCAL_UID}:${LOCAL_GID}" /app /cache_logs

WORKDIR /app

COPY --from=build /app/server /app/server
COPY --from=build /app/BOT_HASH /app/BOT_HASH

USER ${LOCAL_UID}:${LOCAL_GID}

EXPOSE 8080
CMD ["/app/server"]
