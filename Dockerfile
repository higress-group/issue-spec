# syntax=docker/dockerfile:1.7
FROM node:24.5.0-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.25.0-alpine AS build
WORKDIR /src
RUN apk add --no-cache ca-certificates git
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
COPY --from=web /src/web/dist /tmp/web-dist
RUN go run ./internal/server/staticui/cmd/generate /tmp/web-dist ./internal/server/staticui \
    && gofmt -w internal/server/staticui/manifest_gen.go \
    && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w -buildid=' -o /out/issue-spec-server ./cmd/issue-spec-server

FROM gcr.io/distroless/static-debian12:nonroot AS runtime
COPY --from=build --chown=65532:65532 /out/issue-spec-server /issue-spec-server
USER 65532:65532
WORKDIR /var/empty
ENV LISTEN_ADDR=:8080
ENV ISSUE_SPEC_HEALTHCHECK_URL=http://127.0.0.1:8080/readyz
EXPOSE 8080
VOLUME ["/tmp"]
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=6 CMD ["/issue-spec-server", "healthcheck"]
ENTRYPOINT ["/issue-spec-server"]
