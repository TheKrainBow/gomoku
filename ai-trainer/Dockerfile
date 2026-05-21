FROM golang:1.22-alpine AS build

WORKDIR /app
COPY . ./
RUN if [ ! -f go.mod ]; then go mod init gomoku-ai-trainer; fi \
    && go mod tidy \
    && go build -o ai-trainer .

FROM alpine:3.20

ARG LOCAL_UID=1000
ARG LOCAL_GID=1000

WORKDIR /app
COPY --from=build /app/ai-trainer /app/ai-trainer
RUN mkdir -p /logs \
    && chown -R "${LOCAL_UID}:${LOCAL_GID}" /app /logs

USER ${LOCAL_UID}:${LOCAL_GID}

ENV BACKEND_URL=http://backend:8080
ENV POLL_INTERVAL_MS=2000
ENV TRAINER_API_ADDR=:8090

ENTRYPOINT ["/app/ai-trainer"]
