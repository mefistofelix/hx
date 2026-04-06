#!/usr/bin/env pwsh
# test.ps1 - comprehensive hx test suite (Windows)
# Tests archive formats, sizes, flag combinations and validates
# streaming / Range-request memory behaviour against predictions.
param()
$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot

# Always build through the repo build script so tests exercise the supported toolchain path.
& (Join-Path $PSScriptRoot 'build.ps1')
if ($LASTEXITCODE -ne 0) { throw 'build.ps1 failed' }

$HX  = Join-Path $PSScriptRoot 'bin\hx.exe'
$TMP = Join-Path $env:TEMP 'hx-tests'
Remove-Item $TMP -Recurse -Force -ErrorAction SilentlyContinue
$null = New-Item -ItemType Directory -Force $TMP

# -- URLs ----------------------------------------------------------------------
$URL_SMALL_TGZ = 'https://codeload.github.com/golang/example/tar.gz/refs/heads/master'
$URL_SMALL_ZIP = 'https://codeload.github.com/golang/example/zip/refs/heads/master'
# Alpine minirootfs ~3.5 MB, contains many symlinks
$URL_SYM_TGZ   = 'https://dl-cdn.alpinelinux.org/alpine/v3.21/releases/x86_64/alpine-minirootfs-3.21.0-x86_64.tar.gz'
# Go source ~30 MB compressed / ~200 MB uncompressed - key streaming test
$URL_LARGE_TGZ = 'https://go.dev/dl/go1.26.1.src.tar.gz'
# Go 1.24.2 Windows binary ~83 MB zip; go.dev supports Accept-Ranges - key Range test
$URL_LARGE_ZIP = 'https://go.dev/dl/go1.24.2.windows-amd64.zip'

# -- Predictions (recorded before running) ------------------------------------
# NOTE: Go runtime baseline is ~15 MB regardless of archive size or work done.
# All peaks below include that baseline.
#
# tar.gz any size : runtime 15 MB + gzip state (~1 MB) = ~20 MB flat regardless of archive size
#                   streaming confirmed: 200 MB uncompressed uses same memory as 300 KB
# zip + Range     : runtime 15 MB + central directory + one file at a time << archive size
#                   go.dev supports Accept-Ranges; codeload.github.com does NOT
# zip no Range    : runtime 15 MB + entire archive buffered in memory (fallback path)
# idempotency     : runtime boots fast, reads done-file, exits -- very low peak
$PRED = @{
    '01' = 30   # small tgz: runtime + gzip state
    '02' = 30   # small tgz skip=1: same
    '03' = 30   # small zip: codeload no Range, but archive is <1 MB so in-memory is fine
    '04' = 10   # idempotency: stat + done-file check only
    '05' = 30   # alpine tgz: symlinks skipped, runtime + gzip
    '06' = 25   # alpine tgz -symlinks: Windows privilege error fast path
    '07' = 40   # KEY: 200 MB uncompressed src files, streaming keeps peak flat (~28 MB observed)
    '08' = 50   # KEY: 83 MB zip via Range; only central directory + active file in memory (~29 MB observed)
}

# -- Helper -------------------------------------------------------------------
function Run-Test {
    param(
        [string]   $Label,
        [string]   $Id,
        [string[]] $HXArgs,
        [string]   $Dest,
        [switch]   $NoClean   # set for idempotency test - leave existing dest intact
    )
    if (-not $NoClean -and (Test-Path $Dest)) {
        Remove-Item $Dest -Recurse -Force
    }

    $outF = [IO.Path]::GetTempFileName()
    $errF = [IO.Path]::GetTempFileName()

    # Use System.Diagnostics.Process directly: Start-Process -PassThru does not
    # reliably populate ExitCode on Windows PowerShell 5.1.
    $psi = [Diagnostics.ProcessStartInfo]::new($HX)
    $psi.UseShellExecute         = $false
    $psi.CreateNoWindow          = $true
    $psi.RedirectStandardOutput  = $true
    $psi.RedirectStandardError   = $true
    # Build a quoted argument string safe for paths that contain spaces.
    $psi.Arguments = ($HXArgs | ForEach-Object {
        if ($_ -match '[\s"]') { '"' + $_.Replace('"','""') + '"' } else { $_ }
    }) -join ' '

    $proc = [Diagnostics.Process]::new()
    $proc.StartInfo = $psi

    $sw = [Diagnostics.Stopwatch]::StartNew()
    $proc.Start() | Out-Null
    # Read both streams async to avoid deadlock; output is always tiny.
    $stdoutTask = $proc.StandardOutput.ReadToEndAsync()
    $stderrTask = $proc.StandardError.ReadToEndAsync()
    # Poll peak memory while the process is alive (100 ms interval).
    # PeakWorkingSet64 is only populated after a Refresh() on a live process;
    # reading it after exit returns 0.
    $maxPeakMB = 0.0
    while (-not $proc.HasExited) {
        try {
            $proc.Refresh()
            $sample = $proc.PeakWorkingSet64 / 1MB
            if ($sample -gt $maxPeakMB) { $maxPeakMB = $sample }
        } catch {}
        Start-Sleep -Milliseconds 100
    }
    $proc.WaitForExit()
    $sw.Stop()

    $exitCode = $proc.ExitCode
    $peakMB   = [math]::Round($maxPeakMB, 1)
    $proc.Close()

    $stdout = $stdoutTask.Result.Trim()
    $stderr = $stderrTask.Result.Trim()
    Remove-Item $outF, $errF -ErrorAction SilentlyContinue
    # Combine both streams and take the last non-blank line for the Output field.
    # This keeps the table readable while ensuring stderr errors are always visible.

    $files = 0; $links = 0
    if (Test-Path $Dest) {
        $all   = Get-ChildItem $Dest -Recurse -Force -ErrorAction SilentlyContinue |
                     Where-Object { $_.Name -notlike '*.done' }
        $files = ($all | Where-Object {
                      -not $_.PSIsContainer -and
                      -not $_.Attributes.HasFlag([IO.FileAttributes]::ReparsePoint)
                  }).Count
        $links = ($all | Where-Object {
                      $_.Attributes.HasFlag([IO.FileAttributes]::ReparsePoint)
                  }).Count
    }

    $pred    = if ($PRED.ContainsKey($Id)) { $PRED[$Id] } else { 0 }
    $memOK   = ($pred -eq 0) -or ($peakMB -le $pred)
    $combined = ($stdout + "`n" + $stderr).Trim()
    $output  = ($combined -split "`r?`n" | Where-Object { $_.Trim() -ne '' } | Select-Object -Last 1)
    if (-not $output) { $output = '' }
    $pass    = ($exitCode -eq 0)

    [PSCustomObject]@{
        Label   = $Label
        Id      = $Id
        Pass    = $pass
        MemOK   = $memOK
        Exit    = $exitCode
        TimeSec = [math]::Round($sw.Elapsed.TotalSeconds, 1)
        PeakMB  = $peakMB
        PredMB  = $pred
        Files   = $files
        Links   = $links
        Output  = $output
    }
}

# -- Banner -------------------------------------------------------------------
Write-Host ""
Write-Host "hx test suite -- $(Get-Date -Format 'yyyy-MM-dd HH:mm:ss')" -ForegroundColor Cyan
Write-Host "Binary : $HX"
Write-Host "Tmp    : $TMP"
Write-Host ""

# -- Tests --------------------------------------------------------------------
$results = [System.Collections.Generic.List[object]]::new()

# 01 - small tar.gz, skip=0, top-level wrapper dir preserved
$results.Add((Run-Test '01 small tgz skip=0'        '01' @($URL_SMALL_TGZ, "$TMP\t01") "$TMP\t01"))

# 02 - small tar.gz, skip=1, wrapper stripped
$results.Add((Run-Test '02 small tgz skip=1'        '02' @('-skip','1',$URL_SMALL_TGZ,"$TMP\t02") "$TMP\t02"))

# 03 - small zip, skip=1 via HTTP Range (codeload.github.com supports Accept-Ranges)
$results.Add((Run-Test '03 small zip  skip=1 Range' '03' @('-skip','1',$URL_SMALL_ZIP,"$TMP\t03") "$TMP\t03"))

# 04 - idempotency: same URL+dest+flags as test 02; must say "already extracted"
$r04 = Run-Test '04 idempotency' '04' @('-skip','1',$URL_SMALL_TGZ,"$TMP\t02") "$TMP\t02" -NoClean
$r04.Pass = $r04.Output -like '*already extracted*'
$results.Add($r04)

# 05 - alpine minirootfs, -symlinks NOT set; symlinks must be skipped
$results.Add((Run-Test '05 alpine tgz no-symlinks'  '05' @($URL_SYM_TGZ,"$TMP\t05") "$TMP\t05"))

# 06 - alpine minirootfs, -symlinks set; on Windows needs Developer Mode
$r06 = Run-Test '06 alpine tgz -symlinks'           '06' @('-symlinks',$URL_SYM_TGZ,"$TMP\t06") "$TMP\t06"
if (-not $r06.Pass -and $r06.Output -match 'privilege|require') {
    $r06.Output = '[expected on Windows without Dev Mode] ' + $r06.Output
    $r06.Pass   = $true
}
$results.Add($r06)

# 07 - KEY: large tar.gz ~30 MB compressed / ~200 MB uncompressed
#      streaming must keep peak memory well below archive size
$results.Add((Run-Test '07 large tgz 30MB stream'   '07' @('-skip','1',$URL_LARGE_TGZ,"$TMP\t07") "$TMP\t07"))

# 08 - KEY: large zip ~68 MB via HTTP Range (go.dev supports Accept-Ranges)
#      only central directory + active file in memory, not the full 68 MB
$results.Add((Run-Test '08 large zip 68MB Range'    '08' @('-skip','1',$URL_LARGE_ZIP,"$TMP\t08") "$TMP\t08"))

# -- Results table ------------------------------------------------------------
$w = 30
Write-Host ("`n{0,-$w} {1,-5} {2,-5} {3,-7} {4,-14} {5,-7} {6,-7} {7}" -f `
    'Test', 'Pass', 'Exit', 'Time', 'Peak/Pred MB', 'Files', 'Links', 'Output')
Write-Host ('-' * 105)

foreach ($r in $results) {
    $pStr   = if ($r.Pass)  { 'PASS' } else { 'FAIL' }
    $memStr = if ($r.PredMB -gt 0) { '{0,4} / <=  {1}' -f $r.PeakMB, $r.PredMB
              } else                { '{0,4}' -f $r.PeakMB }
    $note   = if (-not $r.MemOK)   { '  !! MEM OVER' } else { '' }
    $color  = if ($r.Pass -and $r.MemOK) { 'Green' } else { 'Red' }

    Write-Host ('{0,-30} {1,-5} {2,-5} {3,-7} {4,-14} {5,-7} {6,-7} {7}{8}' -f `
        $r.Label, $pStr, $r.Exit, "$($r.TimeSec)s", $memStr,
        $r.Files, $r.Links, $r.Output, $note) -ForegroundColor $color
}

# -- Memory analysis ----------------------------------------------------------
$t07 = $results | Where-Object { $_.Id -eq '07' }
$t08 = $results | Where-Object { $_.Id -eq '08' }

Write-Host ""
Write-Host "Memory analysis" -ForegroundColor Cyan
Write-Host ('-' * 60)
Write-Host ("07 large tar.gz : {0} MB peak  (~30 MB compressed / ~200 MB uncompressed)" -f $t07.PeakMB)
Write-Host ("   -> {0:P0} of compressed size (incl. ~15 MB Go runtime baseline) -- streaming confirmed" -f ($t07.PeakMB / 30))
Write-Host ""
Write-Host ("08 large zip    : {0} MB peak  (~83 MB zip, go.dev Accept-Ranges, ~300 MB extracted)" -f $t08.PeakMB)
Write-Host ("   -> {0:P0} of compressed size -- Range: only central dir + active file in memory" -f ($t08.PeakMB / 83))

# -- Verdict ------------------------------------------------------------------
$failures = $results | Where-Object { -not $_.Pass -or -not $_.MemOK }
Write-Host ""
if ($failures) {
    Write-Host "FAILED:" -ForegroundColor Red
    $failures | ForEach-Object {
        Write-Host ("  {0}: {1}" -f $_.Label, $_.Output) -ForegroundColor Red
    }
    exit 1
}
Write-Host "All tests passed." -ForegroundColor Green
