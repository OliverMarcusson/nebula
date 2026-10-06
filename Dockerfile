# Nebula server with the dashboard embedded.
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
COPY --from=web /src/web/dist ./web/dist
ARG VERSION=docker
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /nebula ./cmd/nebula && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /nebula /nebula
# A new volume takes this directory's ownership, so the nonroot user can write it.
COPY --from=build --chown=65532:65532 /out/data /data
VOLUME /data
EXPOSE 13003
ENTRYPOINT ["/nebula"]
CMD ["serve", "--listen", "0.0.0.0:13003", "--data", "/data/archive", "--auth", "/data/credentials/users.json"]
