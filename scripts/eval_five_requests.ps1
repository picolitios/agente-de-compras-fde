param(
    [string]$ApiBaseUrl = 'http://127.0.0.1:8080'
)

$ErrorActionPreference = 'Stop'
$base = $ApiBaseUrl.TrimEnd('/')

# O servidor fornece os mesmos cinco casos e critérios apresentados na interface.
$catalog = Invoke-RestMethod -Uri "$base/avaliacao" -TimeoutSec 10
$cases = @($catalog.casos)
if ($cases.Count -ne 5) { throw "A API devolveu $($cases.Count) casos; eram esperados 5." }
Write-Output "Agente: $($catalog.agente.nome) | Provedor: $($catalog.agente.provedor) | Modelo: $($catalog.agente.modelo)"

# Cada chamada usa uma conversa nova e termina sem confirmar ou registrar uma compra.
$correct = 0
foreach ($case in $cases) {
    try {
        $result = Invoke-RestMethod -Uri "$base/avaliacao/$($case.id)" -Method Post -TimeoutSec 190
        if ($result.acertou) {
            $correct++
            Write-Output "PASS case=$($case.id) expected=$($result.esperado) actual=$($result.obtido)"
        } else {
            Write-Output "FAIL case=$($case.id) expected=$($result.esperado) actual=$($result.obtido) reason=$($result.motivo)"
        }
    } catch {
        Write-Output "FAIL case=$($case.id) error=$($_.Exception.Message)"
    }
}

Write-Output "Acertos: $correct/$($cases.Count)"
if ($correct -ne $cases.Count) { exit 1 }
