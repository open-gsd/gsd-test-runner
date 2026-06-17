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

# Tier-1 watchdog baked alongside the reporter (issue #60, ADR-0021). Run as:
#   node C:/opt/gsd-test/watchdog.mjs --deadline-ms N -- node --test ...
# On Windows the watchdog escalates via taskkill /T /F (whole tree); the
# container-level kill is the cross-platform backstop (ADR-0021 Decision 4).
COPY reporter/watchdog.mjs C:/opt/gsd-test/watchdog.mjs

# Run-and-die entry script (Windows). npm ci + build then the watchdog.
COPY reporter/run-and-die.cmd C:/opt/gsd-test/run-and-die.cmd
COPY reporter/leak-probe.mjs C:/opt/gsd-test/leak-probe.mjs

WORKDIR C:/work
