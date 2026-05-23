# syntax=docker/dockerfile:1.7
# Windows Tester Image. Uses Windows containers (not WSL); requires Windows
# host or a Windows-enabled Docker daemon on the Bench.
FROM mcr.microsoft.com/windows/servercore:ltsc2022

ARG IMAGE_VERSION=dev
LABEL sh.gsd-test.image-version=$IMAGE_VERSION
LABEL org.opencontainers.image.source="https://github.com/open-gsd/gsd-test-runner"
LABEL org.opencontainers.image.description="gsd-test Tester Image (Windows)"

# Install Node 22 (Windows MSI install). PowerShell-based fetch.
SHELL ["powershell", "-Command", "$ErrorActionPreference = 'Stop'; $ProgressPreference = 'SilentlyContinue';"]

RUN $url = 'https://nodejs.org/dist/v22.11.0/node-v22.11.0-x64.msi'; \
    Invoke-WebRequest -UseBasicParsing -Uri $url -OutFile node.msi; \
    Start-Process msiexec.exe -ArgumentList '/i', 'node.msi', '/quiet', '/norestart' -Wait; \
    Remove-Item node.msi

# Git for Windows (minimal portable).
RUN $url = 'https://github.com/git-for-windows/git/releases/download/v2.43.0.windows.1/MinGit-2.43.0-64-bit.zip'; \
    Invoke-WebRequest -UseBasicParsing -Uri $url -OutFile mingit.zip; \
    Expand-Archive mingit.zip -DestinationPath C:\mingit; \
    Remove-Item mingit.zip; \
    setx /M PATH ($env:PATH + ';C:\mingit\cmd;C:\Program Files\nodejs')

COPY reporter/reporter.mjs C:/opt/gsd-test/reporter.mjs

WORKDIR C:/work
