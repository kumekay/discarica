FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /discarica ./cmd/discarica

# distroless/static ships CA certificates (needed for GitHub/Gitea OAuth calls).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /discarica /discarica
EXPOSE 8080
ENTRYPOINT ["/discarica"]
