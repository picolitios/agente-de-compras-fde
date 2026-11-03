param(
    [string]$ApiBaseUrl = 'http://127.0.0.1:8080'
)

$ErrorActionPreference = 'Stop'

# Cada pedido inicia uma conversa independente e termina antes da confirmação da proposta.
$cases = @(
    @{ Name = 'carro_fora_do_orcamento'; Message = 'um carro de 100 dólares'; Type = 'no_match'; ExpectedText = 'Há carros no catálogo' },
    @{ Name = 'carro_dentro_do_orcamento'; Message = '1 carro até 30000 dólares'; Type = 'proposal'; Category = 'vehicle'; Quantity = 1; MaxUnitCents = 3000000 },
    @{ Name = 'laptops'; Message = '3 laptops até 2000 dólares cada'; Type = 'proposal'; Category = 'laptops'; Quantity = 3; MaxUnitCents = 200000 },
    @{ Name = 'celulares_samsung'; Message = '2 celulares Samsung até 1000 dólares'; Type = 'proposal'; Category = 'smartphones'; Quantity = 2; MaxUnitCents = 100000; Brand = 'Samsung' },
    @{ Name = 'produto_indisponivel'; Message = '1 banana até 1 dólar'; Type = 'no_match'; ExpectedText = 'não está disponível' }
)

$correct = 0
foreach ($case in $cases) {
    try {
        $body = @{ mensagem = $case.Message; conversa_id = '' } | ConvertTo-Json -Compress
        $response = Invoke-RestMethod -Uri "$($ApiBaseUrl.TrimEnd('/'))/requisicoes" -Method Post -Body $body `
            -ContentType 'application/json; charset=utf-8' -TimeoutSec 190

        # O acerto exige o tipo de resposta e, nas propostas, requisitos e opções verificadas.
        $valid = $response.tipo -eq $case.Type -and -not [string]::IsNullOrWhiteSpace($response.conversa_id)
        if ($valid -and $case.ContainsKey('ExpectedText')) {
            $valid = $response.mensagem -and $response.mensagem.IndexOf($case.ExpectedText, [StringComparison]::OrdinalIgnoreCase) -ge 0
        }
        if ($valid -and $case.Type -eq 'proposal') {
            $requirements = $response.proposta.requirements
            $products = if ($null -ne $response.produtos) { @($response.produtos) } else { @() }
            $valid = $requirements.category -eq $case.Category -and
                $requirements.quantity -eq $case.Quantity -and
                $requirements.max_unit_cents -eq $case.MaxUnitCents -and
                $products.Count -gt 0
            if ($valid -and $case.ContainsKey('Brand')) {
                $valid = $requirements.required_brand -eq $case.Brand
            }
            foreach ($product in $products) {
                if ($product.category -ne $case.Category -or
                    ($case.ContainsKey('Brand') -and $product.brand -ne $case.Brand)) { $valid = $false }
            }
        }
        $result = if ($valid) { $correct++; 'PASS' } else { 'FAIL' }
        $toolsUsed = if ($response.ferramentas) { $response.ferramentas -join ',' } else { 'nenhuma' }
        Write-Output "$result case=$($case.Name) expected=$($case.Type) actual=$($response.tipo) tools=$toolsUsed"
    } catch {
        $detail = $_.Exception.Message
        Write-Output "FAIL case=$($case.Name) error=$detail"
    }
}

Write-Output "Acertos: $correct/$($cases.Count)"
if ($correct -ne $cases.Count) { exit 1 }
