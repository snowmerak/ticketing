param(
    [int]$VUs = 4,
    [int]$Iterations = 40,
    [int]$WarmupIterations = 5,
    [string]$ResultSuffix = ''
)

$ErrorActionPreference = 'Stop'
if ($VUs -lt 1 -or $VUs -gt 16 -or $Iterations -lt 1 -or $Iterations -gt 100 -or $WarmupIterations -lt 0 -or $WarmupIterations -gt 10) {
    throw 'This local benchmark is bounded to 1-16 VUs, 1-100 measured iterations, and 0-10 warm-up iterations.'
}

$root = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$network = 'ticketing-local_default'
$image = 'grafana/k6@sha256:e66db15b860113878fa74670e31f5e274830b7b6e42c8bff28b2f2d86a257603'
$dsn = 'ticketing:ticketing@tcp(ticketing-local-mysql-1:3306)/ticketing?parseTime=true&loc=UTC&charset=utf8mb4&multiStatements=true'
$eventID = $null
$container = $null
$suffix = if ($ResultSuffix) { "-$ResultSuffix" } else { '' }
if ($ResultSuffix -and $ResultSuffix -notmatch '^[A-Za-z0-9_-]{1,32}$') { throw 'ResultSuffix must be a short filename-safe token.' }

function Assert-ExitCode([string]$operation) {
    if ($LASTEXITCODE -ne 0) { throw "$operation failed with exit code $LASTEXITCODE" }
}

function Run-K6([string]$profile, [int]$count, [int]$seatBase, [string]$outputName) {
    $k6Args = @('run', '--quiet', '--summary-export', "/results/$outputName.json", '/bench/k6.js')
    & docker run --rm --network $network -v "${root}/bench:/bench:ro" -v "${root}/benchmark-results:/results" `
        -e "PROFILE=$profile" -e "RUN_ID=$outputName" -e "EVENT_ID=$eventID" -e "BASE_URL=http://${container}:8080" `
        -e "CAPACITY=64" -e "VUS=$VUs" -e "ITERATIONS=$count" -e "SEAT_BASE=$seatBase" `
        $image @k6Args
    Assert-ExitCode "k6 $outputName"
}

Push-Location $root
try {
    & docker network inspect $network --format '{{.Name}}' | Out-Null
    Assert-ExitCode 'Docker network check'
    & docker image inspect $image --format '{{.Id}}' 2>$null | Out-Null
    if ($LASTEXITCODE -ne 0) {
        & docker pull $image
        Assert-ExitCode 'k6 image pull'
    }
    New-Item -ItemType Directory -Force -Path (Join-Path $root 'bin'), (Join-Path $root 'benchmark-results') | Out-Null
    & go build -o bin/benchfixture.exe ./cmd/benchfixture
    Assert-ExitCode 'fixture build'
    & docker run --rm -v "${root}:/src" -w /src golang:1.27.1-bookworm go build -o /src/bin/ticketing-linux ./cmd/ticketing
    Assert-ExitCode 'Linux service build'

    $eventID = (& .\bin\benchfixture.exe prepare).Trim()
    Assert-ExitCode 'fixture preparation'
    if ($eventID -notmatch '^36[0-9]{8}$') { throw "unexpected fixture event ID: $eventID" }
    $container = "ticketing-bench-$eventID"
    Write-Host "Synthetic event: $eventID; results: $root/benchmark-results"

    & docker run --rm --network $network -v "${root}:/src:ro" -w /src `
        -e "EVENT_IDS=$eventID" -e 'CAPACITY=64' -e 'ADMISSION_RATE=100000' -e 'ADMISSION_BURST=100000' `
        -e 'REDIS_ADDR=ticketing-local-redis-1:6379' -e "MYSQL_DSN=$dsn" `
        golang:1.27.1-bookworm ./bin/ticketing-linux init-state
    Assert-ExitCode 'Redis state initialization'

    & docker run -d --name $container --network $network -p '127.0.0.1::8080' -v "${root}:/src:ro" -w /src `
        -e "EVENT_IDS=$eventID" -e 'CAPACITY=64' -e 'ADMISSION_RATE=100000' -e 'ADMISSION_BURST=100000' `
        -e 'REDIS_ADDR=ticketing-local-redis-1:6379' -e "MYSQL_DSN=$dsn" -e 'TICKETING_LOG_LEVEL=warn' `
        golang:1.27.1-bookworm ./bin/ticketing-linux serve | Out-Null
    Assert-ExitCode 'service container startup'

    $published = (& docker port $container 8080/tcp).Trim()
    Assert-ExitCode 'published port lookup'
    $ready = $false
    for ($attempt = 0; $attempt -lt 100; $attempt++) {
        try {
            $response = Invoke-WebRequest -Uri "http://$published/readyz" -TimeoutSec 1
            if ($response.StatusCode -eq 200) { $ready = $true; break }
        } catch { Start-Sleep -Milliseconds 100 }
    }
    if (-not $ready) { throw 'service did not become ready' }

    $runs = @(
        @{ Profile = 'entry'; Base = 1 },
        @{ Profile = 'seat-map'; Base = 1 },
        @{ Profile = 'hold-cancel'; Base = 1 },
        @{ Profile = 'confirm'; Base = 20; WarmupBase = 1 },
        @{ Profile = 'full'; Base = 150; WarmupBase = 130 }
    )
    foreach ($run in $runs) {
        if ($WarmupIterations -gt 0) {
            $warmupBase = if ($run.ContainsKey('WarmupBase')) { $run.WarmupBase } else { $run.Base }
            $warmupCount = [Math]::Max($WarmupIterations, $VUs)
            Run-K6 $run.Profile $warmupCount $warmupBase "$($run.Profile)-warmup$suffix"
        }
        Run-K6 $run.Profile $Iterations $run.Base "$($run.Profile)$suffix"
    }
    # Queue comes last: filling capacity changes the event into QUEUE mode.
    Run-K6 'queue' $Iterations 1 "queue$suffix"
} finally {
    if ($container) {
        & docker stop $container | Out-Null
        & docker rm $container | Out-Null
    }
    if ($eventID) {
        & .\bin\benchfixture.exe cleanup $eventID
        if ($LASTEXITCODE -ne 0) { Write-Warning "Fixture cleanup failed for event $eventID" }
    }
    Pop-Location
}
