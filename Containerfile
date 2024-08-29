# Use Arch Linux as the base image
FROM archlinux:latest AS builder

# Install necessary dependencies
RUN pacman -Syu --noconfirm \
    base-devel \
    git \
    go \
    libvips \
    ffmpeg \
    vim \
    lsd \
    libheif

# Set the working directory inside the container
WORKDIR /app
COPY . .

ENV CGO_ENABLED=1
RUN go env -w GOCACHE=/go/cache
RUN go env -w GOMODCACHE=/go/modcache
RUN --mount=type=cache,target=/go/modcache go mod download
RUN --mount=type=cache,target=/go/modcache --mount=type=cache,target=/go/cache go build -o /app/timelinize
