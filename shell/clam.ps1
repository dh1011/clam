# clam for PowerShell: when you type an English sentence at the prompt,
# send it to an LLM and stream the answer instead of running it. Anything
# else is left alone and PowerShell behaves normally. Needs PSReadLine
# (bundled with Windows PowerShell 5.1 and PowerShell 7). Load it from
# your $PROFILE with:
#   clam init pwsh | Out-String | Invoke-Expression

$global:_ClamBin = '@CLAM@'
$env:CLAM_SHELL = "PowerShell $($PSVersionTable.PSVersion)"
$env:CLAM_SESSION_ID = "powershell-$PID-$([guid]::NewGuid().ToString('N'))"

# Common English words; lines starting with one are checked even when the
# word happens to be a command or alias (e.g. `where`, `sort`). Loaded on
# first `clam on`.
$global:_ClamWords = $null
if (-not (Get-Variable -Name _ClamEnabled -Scope Global -ErrorAction SilentlyContinue)) {
    $global:_ClamEnabled = $false
}

# Quote one argument for a Windows command line, which is how .NET passes
# arguments to the child on every platform.
function _ClamQuote([string]$Arg) {
    if ($Arg -and $Arg -notmatch '[\s"]') { return $Arg }
    $Arg = $Arg -replace '(\\*)"', '$1$1\"' -replace '(\\+)$', '$1$1'
    return '"' + $Arg + '"'
}

# Run clam attached to the console, so it can draw on it; PowerShell's &
# would capture the output of a key handler. -Quiet discards stdout.
# Returns the exit code.
function _ClamRun([string[]]$Arguments, [switch]$Quiet) {
    $info = New-Object System.Diagnostics.ProcessStartInfo
    $info.FileName = $global:_ClamBin
    $info.Arguments = ($Arguments | ForEach-Object { _ClamQuote $_ }) -join ' '
    $info.UseShellExecute = $false
    $info.RedirectStandardOutput = [bool]$Quiet
    $process = [System.Diagnostics.Process]::Start($info)
    if ($Quiet) { $null = $process.StandardOutput.ReadToEnd() }
    $process.WaitForExit()
    return $process.ExitCode
}

function _ClamLoad {
    $words = & $global:_ClamBin words 2>$null
    if ($LASTEXITCODE -ne 0 -or -not $words) { return $false }
    $global:_ClamWords = New-Object 'System.Collections.Generic.HashSet[string]' (
        [string[]]$words, [System.StringComparer]::OrdinalIgnoreCase)
    return $true
}

# Decide whether the line is English.
function _ClamIsEnglish([string]$Line) {
    $first = ($Line -split '\s+')[0]
    $flags = @()
    if (Get-Command -Name $first -ErrorAction SilentlyContinue) {
        # Known command that isn't an English word: run it without delay.
        if (-not $global:_ClamWords.Contains($first)) { return $false }
        $flags = @('--exists')
    }
    return (_ClamRun (@('check') + $flags + @('--', $Line)) -Quiet) -eq 0
}

# Enter: run anything that isn't English as usual. For English, keep the
# sentence on screen, show the answer below it, then draw a fresh prompt.
$global:_ClamEnter = {
    $line = $null
    $cursor = $null
    [Microsoft.PowerShell.PSConsoleReadLine]::GetBufferState([ref]$line, [ref]$cursor)
    $query = $line.Trim()

    $english = $false
    if ($query) {
        try { $english = _ClamIsEnglish $query } catch { $english = $false }
    }
    if (-not $english) {
        [Microsoft.PowerShell.PSConsoleReadLine]::AcceptLine()
        return
    }

    [Microsoft.PowerShell.PSConsoleReadLine]::AddToHistory($line)
    [Microsoft.PowerShell.PSConsoleReadLine]::EndOfLine()
    [Console]::WriteLine()

    $null = _ClamRun @('ask', '--', $query)

    [Microsoft.PowerShell.PSConsoleReadLine]::InvokePrompt($null, [Console]::CursorTop)
    [Microsoft.PowerShell.PSConsoleReadLine]::RevertLine()
}

# `clam on` / `clam off` toggle it in this session only.
function clam {
    param([string]$State, [string]$Mode, [switch]$Quiet)
    switch ($State) {
        'on' {
            if (-not (Get-Module PSReadLine)) {
                # Non-interactive hosts (scripts, ssh commands) load the
                # profile too; only complain when asked by hand.
                if (-not $Quiet) {
                    Write-Warning 'clam needs PSReadLine, which this host does not load.'
                }
                return
            }
            if (-not $global:_ClamWords -and -not (_ClamLoad)) {
                Write-Warning "clam: cannot run $global:_ClamBin"
                return
            }
            Set-PSReadLineKeyHandler -Chord Enter -ScriptBlock $global:_ClamEnter `
                -BriefDescription 'clam' -Description 'Answer English sentences instead of running them'
            $global:_ClamEnabled = $true
            if (-not $Quiet) { 'clam: on' }
        }
        'off' {
            Set-PSReadLineKeyHandler -Chord Enter -Function AcceptLine
            $global:_ClamEnabled = $false
            'clam: off'
        }
        'agent' {
            if (-not $Mode) {
                & $global:_ClamBin agents
            } else {
                & $global:_ClamBin agents $Mode
                if ($LASTEXITCODE -eq 0) {
                    $env:CLAM_AGENT = $Mode
                    "clam: using $Mode in this session"
                }
            }
        }
        'auto' {
            switch ($Mode) {
                'on' {
                    $env:CLAM_AUTO_APPROVE = '1'
                    'clam: auto-approve on - commands and file writes run without asking'
                }
                'off' {
                    Remove-Item Env:CLAM_AUTO_APPROVE -ErrorAction SilentlyContinue
                    'clam: auto-approve off'
                }
                default { 'usage: clam auto on|off' }
            }
        }
        default {
            $rest = @(@($State, $Mode) + $args | Where-Object { $_ })
            & $global:_ClamBin @rest
        }
    }
}

# Keep the original prompt (including any theme or virtual environment) and
# add an explicit status label when PowerShell draws the next prompt.
if (-not (Get-Variable -Name _ClamOriginalPrompt -Scope Global -ErrorAction SilentlyContinue)) {
    $global:_ClamOriginalPrompt = (Get-Command prompt -CommandType Function).ScriptBlock
}
# Windows Terminal, VS Code and other modern terminals draw color emoji; the
# legacy console host does not. Set $env:CLAM_EMOJI to 1 or 0 to override.
# (Emoji are built from code points so the file stays ASCII for PowerShell 5.1.)
$global:_ClamMarks = if ($env:CLAM_EMOJI -eq '1' -or ($env:CLAM_EMOJI -ne '0' -and
        ($env:WT_SESSION -or $env:TERM_PROGRAM -in 'vscode', 'WezTerm', 'iTerm.app', 'ghostty' -or
         $env:KITTY_WINDOW_ID -or $env:WEZTERM_PANE))) {
    @{ On = [char]::ConvertFromUtf32(0x1F41A); Auto = [char]::ConvertFromUtf32(0x1F525); Off = [char]::ConvertFromUtf32(0x1F4A4) }
} else {
    @{ On = '[c+]'; Auto = '[c!]'; Off = '[c-]' }
}
function global:prompt {
    $marker = if (-not $global:_ClamEnabled) { $global:_ClamMarks.Off }
        elseif ($env:CLAM_AUTO_APPROVE -eq '1') { $global:_ClamMarks.Auto }
        else { $global:_ClamMarks.On }
    return "$marker " + (& $global:_ClamOriginalPrompt)
}

# Set $env:CLAM_DEFAULT = 'off' before loading to start sessions with it off.
if ($env:CLAM_DEFAULT -eq 'off') {
    if ($global:_ClamEnabled) { $null = clam off }
    "clam is off. Run 'clam on' to enable it in this session."
} else {
    clam on -Quiet
}
