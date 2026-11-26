# Evidências

As duas execuções usaram a carga padrão do Compose, com 10 pedidos sequenciais, 200 com 50 em voo e cinco riscos distintos. O ambiente foi recriado entre as execuções. Os horários do contêiner estão em UTC.

| Carga | p95 | Ao menos uma oferta | Vazão |
| --- | ---: | ---: | ---: |
| Antes | 8,01 s | 120 de 200 (60%) | 8,1 req/s |
| Depois | 203 ms | 200 de 200 (100%) | 723,2 req/s |

O ganho de vazão vale para essa carga de riscos repetidos. Os números do TCO usam hipóteses de recotação em produção.

| Artefato | Comando ou consulta | Leitura |
| --- | --- | --- |
| [Antes](antes-carga.txt) e [depois](depois-carga.txt) | `make down && make reproduce` em cada revisão | Mesma carga, com melhora de p95, disponibilidade e vazão na fase concorrente. |
| [Testes](testes.txt) e [factibilidade](smoke.txt) | `make test` e `make smoke` | A suíte do repositório e os testes de isolamento, fallback e circuito passaram. |
| [Trace em série](antes-trace.json) | Jaeger, `quotation-api`, `POST /quotes`, última hora, maior duração | As três chamadas somam aproximadamente 8,06 s. |
| [Trace com corte](breaker-trace.json) | Jaeger, `quotation-api`, `POST /quotes`, última hora, evento `quote.breaker.rejected` | A API não chamou `partner-flaky`. As ofertas disponíveis chegaram em 1,50 s. |
| [Trace de cache](cache-trace.json) | Jaeger, `quotation-api`, `POST /quotes`, última hora, evento `quote.cache.hit` | Três acertos e nenhum span HTTP de saída. O span de servidor durou 0,304 ms. |
| [Transições](breaker.png) | Consulta do breaker abaixo e eventos `quote.breaker.transition` no Jaeger | A degradante passou de fechado a aberto, a meio aberto e voltou a fechado. |
| [Acertos](hit-rate.png) | Consulta de acertos abaixo | A fase com riscos inéditos reduz o acerto. Repetir cinco riscos faz a série subir. |
| [p95 antes](p95-antes.png) e [p95 depois](p95-depois.png) | Consulta p95 abaixo | Histograma acumulado desde a subida, com a mesma escala e os primeiros 85 s após iniciar cada carga. |

## Consultas

p95 de `POST /quotes`. O gráfico do antes foi refeito com uma reprodução limpa da revisão original para usar a mesma consulta acumulada do depois. Os contadores exportam em lotes. O relatório do gerador é a medição precisa da fase sob carga.

```promql
histogram_quantile(0.95, sum by (le) (
  http_server_request_duration_seconds_bucket{http_route="/quotes",http_request_method="POST"}
))
```

Acertos com janela móvel de 25 s e corte de 01:46:10 a 01:47:55 UTC.

```promql
100 * sum(rate(quote_cache_lookups_total{result="hit"}[25s]))
    / sum(rate(quote_cache_lookups_total[25s]))
```

Transições no mesmo corte, com o estado reconstruído pelos eventos do Jaeger. A abertura ocorreu às 01:46:29,868 UTC. A sonda entrou às 01:46:32,906 e fechou às 01:46:33,030.

```promql
sum by (from_state, to_state) (
  quote_breaker_transitions_total{partner="partner-degrading"}
)
```

Os [ensaios de recuperação](sondas.txt) usaram o ambiente da versão protegida. As configurações das seguradoras permaneceram as do Compose.

```sh
make load ARGS="-baseline 0 -requests 150 -distinct 150 -concurrency 14"
sleep 4
make load ARGS="-tenant corretora-b -baseline 0 -requests 8 -distinct 8 -concurrency 1"
make load ARGS="-tenant corretora-b -baseline 10 -requests 200 -distinct 5 -concurrency 50"
```

Para examinar a integração separadamente, agrupe a latência de cliente por destino.

```promql
histogram_quantile(0.95, sum by (server_address, le) (
  rate(http_client_request_duration_seconds_bucket[1m])
))
```
