# Use Arch Linux as the base image
FROM archlinux:latest

# Install necessary dependencies
RUN pacman -Syu --noconfirm \
    base-devel \
    git \
    go \
    libvips \
    ffmpeg \
    vim \
    lsd \
    lsof \
    git \
    openssh \
    zip \
    procps-ng \
    neovim \
    less \
    libheif

RUN curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b /usr/local/bin v1.60.3

RUN echo "%wheel ALL=(ALL) NOPASSWD: ALL" > /etc/sudoers.d/toolbox
