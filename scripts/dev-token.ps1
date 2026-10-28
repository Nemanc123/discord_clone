#Requires -Version 5.1
<#
    Генерация тестового токена LiveKit для локального сервера в режиме --dev.

    В dev-режиме LiveKit использует фиксированные ключи:
        API key:    devkey
        API secret: secret

    Токен нужен только на вход в комнату. В реальном приложении его выдаёт
    Go-сервер после проверки прав (этап 5). Здесь — для спайка.

    Использование:
        $token = .\scripts\dev-token.ps1 -Identity "spike-user" -Room "spike"
        flutter build apk --debug --dart-define=LK_TOKEN=$token
#>
[CmdletBinding()]
param(
    [string]$Identity = "spike-user",
    [string]$Room     = "spike",
    [int]$TtlMinutes  = 120
)

$ErrorActionPreference = "Stop"

$apiKey    = "devkey"
$apiSecret = "secret"

function ConvertTo-Base64Url([byte[]]$Bytes) {
    [Convert]::ToBase64String($Bytes).TrimEnd('=').Replace('+', '-').Replace('/', '_')
}

$now = [DateTimeOffset]::UtcNow.ToUnixTimeSeconds()

$headerJson  = '{"alg":"HS256","typ":"JWT"}'
$payloadJson = (@{
    iss   = $apiKey
    sub   = $Identity
    nbf   = $now - 10
    exp   = $now + ($TtlMinutes * 60)
    video = @{
        room      = $Room
        roomJoin  = $true
        canPublish   = $true
        canSubscribe = $true
    }
} | ConvertTo-Json -Compress -Depth 5)

$headerB64  = ConvertTo-Base64Url ([Text.Encoding]::UTF8.GetBytes($headerJson))
$payloadB64 = ConvertTo-Base64Url ([Text.Encoding]::UTF8.GetBytes($payloadJson))
$unsigned   = "$headerB64.$payloadB64"

$hmac = New-Object System.Security.Cryptography.HMACSHA256
$hmac.Key = [Text.Encoding]::UTF8.GetBytes($apiSecret)
$signature = ConvertTo-Base64Url ($hmac.ComputeHash([Text.Encoding]::UTF8.GetBytes($unsigned)))

Write-Output "$unsigned.$signature"
