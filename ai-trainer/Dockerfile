FROM golang:1.22-alpine AS build

WORKDIR /app
COPY . ./
RUN go mod tidy && go build -o ai-trainer .

FROM alpine:3.20
WORKDIR /app
COPY --from=build /app/ai-trainer /app/ai-trainer
RUN mkdir -p /logs

ENV BACKEND_URL=http://backend:8080
ENV POLL_INTERVAL_MS=2000

ENTRYPOINT ["/app/ai-trainer"]
