#Requires -Version 5.1
<#
    Установка git-хуков проекта.
    Запуск один раз после клонирования:  .\scripts\install-hooks.ps1
#>

$root = Split-Path -Parent $PSScriptRoot

if (-not (Test-Path (Join-Path $root ".git"))) {
    Write-Host "Не найден каталог .git — запустите из корня репозитория." -ForegroundColor Red
    exit 1
}

$source = Join-Path $PSScriptRoot "hooks\pre-commit"
$target = Join-Path $root ".git\hooks\pre-commit"

Copy-Item $source $target -Force

Write-Host "Установлен хук: $target" -ForegroundColor Green
Write-Host "Проверка: измените файл и попробуйте закоммитить без форматирования." -ForegroundColor DarkGray
