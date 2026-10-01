FROM node:22.14.0-alpine AS console
WORKDIR /src/ui
COPY ui/package.json ui/package-lock.json ./
RUN npm ci --ignore-scripts
COPY ui/ ./
RUN npm run build:llmhub

FROM golang:1.26.5-alpine AS gateway
RUN apk add --no-cache gcc musl-dev
WORKDIR /src/hub
COPY hub/go.mod hub/go.sum ./
RUN go mod download
COPY hub/ ./
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /llmhub ./cmd/llmhub

FROM alpine:3.23
RUN apk add --no-cache ca-certificates && addgroup -g 10001 hub && adduser -D -u 10001 -G hub hub && mkdir /data && chown hub:hub /data
COPY --from=gateway /llmhub /usr/local/bin/llmhub
COPY --from=console /src/ui/llmhub/dist /app/ui
COPY deploy/llmhub/seed.json /app/seed.json
USER 10001:10001
EXPOSE 8080
VOLUME /data
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD wget -q -O /dev/null http://127.0.0.1:8080/health || exit 1
ENTRYPOINT ["/usr/local/bin/llmhub"]
CMD ["-listen", "0.0.0.0:8080", "-db", "/data/llmhub.db", "-bifrost", "http://bifrost:8080", "-ui", "/app/ui", "-seed", "/app/seed.json"]
