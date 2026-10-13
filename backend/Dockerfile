FROM golang:1.26-alpine AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/api ./cmd/api && CGO_ENABLED=0 go build -trimpath -o /out/worker ./cmd/worker

FROM alpine:3.23
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 tokenswoop
WORKDIR /app
COPY --from=build /out/ /app/
COPY LICENSE NOTICE.md /app/licenses/
COPY third_party/relaykit/NOTICE /app/licenses/RELAYKIT-NOTICE
USER tokenswoop
EXPOSE 8080
ENTRYPOINT ["/app/api"]
