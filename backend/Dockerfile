FROM golang:1.22-alpine

WORKDIR /app

COPY . ./
RUN if [ ! -f go.mod ]; then go mod init gomoku-backend; fi \
    && go mod tidy \
    && go build -o server .

EXPOSE 8080
CMD ["/app/server"]
