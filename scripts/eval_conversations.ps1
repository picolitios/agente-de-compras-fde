param(
    [string]$ApiBaseUrl = 'http://127.0.0.1:8080',
    [ValidateRange(1, 20)][int]$Repeat = 1
)

$ErrorActionPreference = 'Stop'

# Os cenários exercitam pedidos novos, esclarecimentos e mudanças de escopo sem confirmar compras.
$scenarios = @(
    @{ Name = 'preco_e_quantidade'; Turns = @(
        @{ Message = 'carro 100 dolares'; Type = 'no_match' },
        @{ Message = 'aumente pra 25000,00, preciso de um carro só'; Type = 'proposal'; Category = 'vehicle'; Quantity = 1; MaxUnitCents = 2500000 }
    ) },
    @{ Name = 'quantidade_em_outra_mensagem'; Turns = @(
        @{ Message = 'carros até 100 dólares'; Type = 'clarify' },
        @{ Message = 'quero um carro só'; Type = 'no_match' },
        @{ Message = 'pode ser 25000,00'; Type = 'proposal'; Category = 'vehicle'; Quantity = 1; MaxUnitCents = 2500000 }
    ) },
    @{ Name = 'quantidade_e_preco_juntos'; Turns = @(
        @{ Message = 'carros até 100 dólares'; Type = 'clarify' },
        @{ Message = 'um carro, pode ser 25000'; Type = 'proposal'; Category = 'vehicle'; Quantity = 1; MaxUnitCents = 2500000 }
    ) },
    @{ Name = 'ajustes_repetidos'; Turns = @(
        @{ Message = 'carro até 100 dólares'; Type = 'no_match' },
        @{ Message = 'pode ser 25000,00 então'; Type = 'proposal'; Category = 'vehicle'; Quantity = 1; MaxUnitCents = 2500000 },
        @{ Message = 'aumente para 30000,00'; Type = 'proposal'; Category = 'vehicle'; Quantity = 1; MaxUnitCents = 3000000 },
        @{ Message = 'aumento pra 60000,00'; Type = 'proposal'; Category = 'vehicle'; Quantity = 1; MaxUnitCents = 6000000 }
    ) },
    @{ Name = 'troca_de_categoria'; Turns = @(
        @{ Message = 'carros até 100 dólares'; Type = 'clarify' },
        @{ Message = 'quero 2 celulares até 1000 dólares'; Type = 'proposal'; Category = 'smartphones'; Quantity = 2; MaxUnitCents = 100000 }
    ) },
    @{ Name = 'laptops'; Turns = @(
        @{ Message = '3 laptops até 2000 dólares cada'; Type = 'proposal'; Category = 'laptops'; Quantity = 3; MaxUnitCents = 200000 }
    ) },
    @{ Name = 'moeda_diferente'; Turns = @(
        @{ Message = '1 carro até 100 reais'; Type = 'clarify' }
    ) },
    @{ Name = 'criterio_nao_verificavel'; Turns = @(
        @{ Message = 'carros elétricos até 30000 dólares'; Type = 'clarify' }
    ) },
    @{ Name = 'produto_ausente'; Turns = @(
        @{ Message = '1 banana até 1 dólar'; Type = 'no_match' }
    ) }
)

# Cada cenário recebe um ID de conversa novo; falhas HTTP ficam visíveis sem repetir a mensagem.
$failures = 0
for ($run = 1; $run -le $Repeat; $run++) {
    foreach ($scenario in $scenarios) {
        $conversationId = ''
        foreach ($turn in $scenario.Turns) {
            try {
                $body = @{ mensagem = $turn.Message; conversa_id = $conversationId } | ConvertTo-Json -Compress
                $response = Invoke-RestMethod -Uri "$ApiBaseUrl/requisicoes" -Method Post -Body $body `
                    -ContentType 'application/json; charset=utf-8' -TimeoutSec 190 -ErrorAction Stop
            } catch {
                $code = 'HTTP_ERROR'
                try { $code = ($_.ErrorDetails.Message | ConvertFrom-Json).erro.codigo } catch {}
                Write-Output "FAIL run=$run case=$($scenario.Name) code=$code"
                $failures++
                break
            }
            $optionCount = if ($null -ne $response.produtos) { @($response.produtos).Count } else { 0 }
            $sameConversation = $conversationId -eq '' -or $conversationId -eq $response.conversa_id
            $wrongRequirements = $false
            if ($turn.Type -eq 'proposal') {
                $requirements = $response.proposta.requirements
                $wrongRequirements = $requirements.category -ne $turn.Category -or
                    $requirements.quantity -ne $turn.Quantity -or
                    $requirements.max_unit_cents -ne $turn.MaxUnitCents
                foreach ($product in $response.produtos) {
                    if ($product.category -ne $turn.Category) { $wrongRequirements = $true }
                }
            }
            if ($response.tipo -ne $turn.Type -or !$sameConversation -or
                ($turn.Type -eq 'proposal' -and $optionCount -lt 1) -or $wrongRequirements) {
                Write-Output "FAIL run=$run case=$($scenario.Name) expected=$($turn.Type) actual=$($response.tipo) options=$optionCount"
                $failures++
                break
            }
            $conversationId = $response.conversa_id
            Write-Output "PASS run=$run case=$($scenario.Name) type=$($response.tipo) options=$optionCount"
        }
    }
}

if ($failures -gt 0) { throw "$failures falha(s) na avaliação de conversas" }
Write-Output "Avaliação concluída: $Repeat repetição(ões), $($scenarios.Count) cenários por repetição."
