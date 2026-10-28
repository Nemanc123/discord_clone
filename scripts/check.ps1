#Requires -Version 5.1
<#
    Полная локальная проверка проекта (уровень 2 из трёх).

    Запуск:  .\scripts\check.ps1
    Время:   ~1 минута

    Совместим с PowerShell 5.1 (встроенный в Windows) и PowerShell 7.
    Тот же скрипт вызывает CI — чтобы не было двух разных правд.
#>
[CmdletBinding()]
param()

$ErrorActionPreference = "Continue"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

$failed = New-Object System.Collections.Generic.List[string]

function Report([string]$Name, [bool]$Ok, [string]$Detail) {
    if ($Ok) {
        Write-Host ("  ok    {0}" -f $Name) -ForegroundColor Green
    } else {
        Write-Host ("  FAIL  {0} {1}" -f $Name, $Detail) -ForegroundColor Red
        $script:failed.Add($Name)
    }
}

Write-Host "== Go ==" -ForegroundColor Cyan
if (Get-Command go -ErrorAction SilentlyContinue) {
    $unformatted = @(gofmt -l .)
    Report "gofmt" ($unformatted.Count -eq 0) ($unformatted -join ", ")

    go vet ./...
    Report "go vet" ($LASTEXITCODE -eq 0) ""

    go test ./...
    Report "go test" ($LASTEXITCODE -eq 0) ""

    go build ./...
    Report "go build" ($LASTEXITCODE -eq 0) ""

    if (Get-Command golangci-lint -ErrorAction SilentlyContinue) {
        golangci-lint run
        Report "golangci-lint" ($LASTEXITCODE -eq 0) ""
    } else {
        Write-Host "  skip  golangci-lint (не установлен)" -ForegroundColor DarkGray
    }
} else {
    Write-Host "  skip  Go не найден в PATH" -ForegroundColor DarkGray
}

Write-Host "== Flutter ==" -ForegroundColor Cyan
if ((Test-Path "app\pubspec.yaml") -and (Get-Command flutter -ErrorAction SilentlyContinue)) {
    Push-Location app
    flutter analyze
    Report "flutter analyze" ($LASTEXITCODE -eq 0) ""
    flutter test
    Report "flutter test" ($LASTEXITCODE -eq 0) ""
    Pop-Location
} else {
    Write-Host "  skip  клиент пока не создан" -ForegroundColor DarkGray
}

Write-Host ""
if ($failed.Count -gt 0) {
    Write-Host ("ПРОВЕРКИ НЕ ПРОЙДЕНЫ: {0}" -f ($failed -join ", ")) -ForegroundColor Red
    exit 1
}

Write-Host "Всё зелёное" -ForegroundColor Green
exit 0
