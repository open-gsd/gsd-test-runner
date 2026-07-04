# syntax=docker/dockerfile:1.7
# Windows Tester Image. Uses Windows containers (not WSL); requires Windows
# host or a Windows-enabled Docker daemon on the Bench.
FROM mcr.microsoft.com/windows/servercore:ltsc2022

ARG IMAGE_VERSION=dev
# NODE_VERSION selects the Node major baked into this image (Slice 1 of
# enhancement #108). Default stays 22 so a plain `docker build` is unaffected.
ARG NODE_VERSION=22
LABEL sh.gsd-test.image-version=$IMAGE_VERSION
# Companion sentinel to image-version (mirrors ADR-0011): records the Node
# major this image was built with, so consumers can select/verify by major.
LABEL sh.gsd-test.node-major=$NODE_VERSION
LABEL org.opencontainers.image.source="https://github.com/open-gsd/gsd-test-runner"
LABEL org.opencontainers.image.description="gsd-test Tester Image (Windows)"

# Install Node (Windows MSI install), resolved to the latest patch release
# for NODE_VERSION's major at build time. PowerShell-based fetch.
SHELL ["powershell", "-Command", "$ErrorActionPreference = 'Stop'; $ProgressPreference = 'SilentlyContinue';"]

# Re-declare NODE_VERSION here: ARG scope doesn't cross the intervening
# SHELL instruction implicitly for this RUN's $env: lookup, and RUN needs
# the ARG in-scope to expose it as an environment variable.
ARG NODE_VERSION
# NOTE: brace the env var as ${env:NODE_VERSION} inside the wildcard string.
# A bare "$env:NODE_VERSION.*" makes PowerShell's tokenizer fail to bound the
# variable name before the '.', throwing `Unexpected token` / ExpectedValue-
# Expression and aborting the whole image build (regression: publish-windows
# failed on every tag through v1.6.0). The braces delimit the name so '.*' is
# literal. Other $env:NODE_VERSION uses below are fine — none are followed by '.'.
RUN $index = Invoke-RestMethod -UseBasicParsing -Uri 'https://nodejs.org/dist/index.json'; \
    $match = $index | Where-Object { $_.version -like "v${env:NODE_VERSION}.*" } | Select-Object -First 1; \
    if (-not $match) { Write-Error "No Node release found for major version $env:NODE_VERSION"; exit 1 }; \
    $ver = $match.version; \
    Write-Host "Resolved Node $env:NODE_VERSION -> $ver"; \
    $url = "https://nodejs.org/dist/$ver/node-$ver-x64.msi"; \
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
