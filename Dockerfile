FROM golang:1.25.4 AS builder

WORKDIR /src

COPY go.mod ./
COPY . ./

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/chataibot2api .

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

COPY --from=builder /out/chataibot2api /app/chataibot2api

ENV PORT=8080

EXPOSE 8080

ENTRYPOINT ["/app/chataibot2api"]
