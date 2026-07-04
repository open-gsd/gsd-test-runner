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

# Re-declare NODE_VERSION here so it is in scope for Docker's ${NODE_VERSION}
# substitution in the RUN below (ARG scope resets after the FROM/SHELL layers).
ARG NODE_VERSION
# IMPORTANT — Windows RUN quoting: this is a shell-form RUN under an exec-form
# `SHELL ["powershell","-Command",...]`. Docker appends the RUN command to that
# argv, and Windows' CommandLineToArgvW strips *double* quotes before PowerShell
# ever parses the script — so a double-quoted string like "v...*" arrives as a
# bare word and the build dies with `Unexpected token` / ExpectedValueExpression
# (regression: publish-windows failed on every tag through v1.6.1). Single quotes
# survive transport untouched, which is why the git RUN below always worked.
# Rules for this block:
#   1. Use ONLY single-quoted strings (never double quotes).
#   2. Inject the Node major via Docker's ${NODE_VERSION} ARG substitution
#      (baked in at build time), NOT PowerShell's $env:NODE_VERSION — an ARG is
#      not exposed to the process environment, so $env:NODE_VERSION is empty.
#   3. Splice runtime values ($ver) with string concatenation, not interpolation.
RUN $index = Invoke-RestMethod -UseBasicParsing -Uri 'https://nodejs.org/dist/index.json'; \
    $match = $index | Where-Object { $_.version -like 'v${NODE_VERSION}.*' } | Select-Object -First 1; \
    if (-not $match) { Write-Error 'No Node release found for major version ${NODE_VERSION}'; exit 1 }; \
    $ver = $match.version; \
    Write-Host ('Resolved Node ${NODE_VERSION} -> ' + $ver); \
    $url = 'https://nodejs.org/dist/' + $ver + '/node-' + $ver + '-x64.msi'; \
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
