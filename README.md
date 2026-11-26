# Prumo Cota

O Prumo Cota agrega propostas de três seguradoras. A API consulta as três em paralelo e devolve, da mais barata para a mais cara, as ofertas que chegaram dentro do prazo ou continuam válidas no Redis. Cada seguradora tem um circuito próprio. Se faltar alguma, a resposta indica quais ficaram de fora. Isso troca a falha de toda a operação pelo custo de administrar o cache, seus prazos e a recuperação por parceira.

O [SAD](docs/sad.md) documenta as escolhas, a conta de custo e a proposta de auditoria de produção. O [índice de evidências](docs/evidencias/README.md) reúne a carga, os traces, os gráficos e os testes. O armazenamento imutável e a redundância de produção são propostas do SAD.

## Rodar

Use Go 1.25+ e Docker com Compose v2.

```sh
make test
make smoke
make down
make reproduce
```

A API sobe em `localhost:8080`, o Jaeger em `localhost:16686` e o Prometheus em `localhost:9090`. `make reproduce` usa 10 cotações sequenciais e 200 com 50 em voo. Use um ambiente zerado para comparar a sequência das seguradoras.

A chave do Redis é `quotation:v1:<tenant_id>:<partner>:<hmac_sha256(normalized_risk_json)>`. Corretora e parceira passam por codificação de URL. A oferta vence em até quatro minutos, respeitando a validade do parceiro. O processo gera uma chave HMAC efêmera por padrão. Para compartilhar cache entre réplicas, configure `CACHE_KEY_SECRET` com o mesmo segredo de pelo menos 32 bytes.

O timeout por seguradora é de 2 s. O circuito abre após três falhas consecutivas em 12 s, espera 3 s e autoriza uma sonda. `sources` informa origem e instante de cada oferta, com idade em segundos para cache. `degraded` e `missing_partners` indicam resposta parcial. Se nenhuma seguradora fornecer oferta, o endpoint retorna 503 com `error: "no_quotes_available"` e `next_action: "retry"`.

## Telemetria

| Sinal | Campos |
| --- | --- |
| `quote_cache_lookups_total` | `partner`, `result` (`hit` ou `miss`) |
| `quote_breaker_transitions_total` | `partner`, `from_state`, `to_state` |
| `http_client_request_duration_seconds_bucket` | Histograma de latência por `server_address` já fornecido pelo SDK |
| Eventos de span | `quote.cache.hit`, `quote.cache.read_error`, `quote.cache.write_error`, `quote.breaker.transition`, `quote.breaker.rejected` |

Num piloto, mediria primeiro quantas recotações realmente se repetem em quatro minutos por corretora. Depois testaria rajadas de riscos novos sem cache, que são as que mais pressionam a seguradora sensível à concorrência.
