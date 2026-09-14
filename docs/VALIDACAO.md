# Validação da entrega

Data: **14/09/2026**. Ambiente local: Windows amd64, Go 1.27.1. Testes executados com dados artificiais em diretórios temporários. Nenhuma conta do usuário foi vinculada e nenhuma mensagem foi transmitida a um destinatário.

## Resultados observados

| Verificação | Resultado |
|---|---|
| `go test -count=1 -cover ./...` | 29 testes passaram |
| `go vet ./...` | Passou |
| `gofmt` | Aplicado ao código Go |
| Build Windows amd64 | Passou; aplicativo iniciado e utilizado localmente |
| Build Linux amd64, CGO_ENABLED=0 | Passou; binário compilado com Go 1.27.1 |
| `node --check web/app.js` | Passou |
| `bash -n scripts/setup.sh scripts/backup.sh` | Passou |
| Login, painel e consulta da fila em navegador real | Funcionaram |
| Pausar fila, adicionar e cancelar trabalho sem conta vinculada | Funcionaram; nenhuma mensagem foi transmitida |
| Reiniciar app e consultar pausa/histórico | Persistência confirmada |
| Solicitar QR Code ao WhatsApp | QR recebido e exibido; permaneceu ativo após o retorno HTTP |
| Encerrar pareamento de teste | Pausa confirmada no painel |
| `govulncheck` | **Não concluído**: o ambiente bloqueou rede na execução final; não há declaração de ausência de vulnerabilidades |
| Docker build / Compose / TLS na VPS | **Não executados localmente**: Docker/VPS não disponíveis nesta sessão |
| Race detector | Configurado na CI Linux; não executado no Windows local sem toolchain C |

Cobertura observada: core **67,9%**, httpapi **50,5%**, wa **24,7%**; main **0%** no relatório unitário (startup foi exercitado manualmente). Esses números são cobertura de instruções, não percentual de confiabilidade. Não se afirma cobertura completa de rede, protocolo, dispositivos ou falhas de hardware.

## O que os testes verificam

Idempotência sob concorrência; conflito de conteúdo; payload cifrado e adulteração; rejeição de chave incorreta; recuperação de envio interrompido; recibos fora de ordem; deduplicação de eventos; dead letter e reprocessamento; retenção preservando idempotência; fila cheia; ausência de repetição automática após timeout; modo offline; falha de preparação de arquivo; expiração; recibo durante o envio; pausa/cancelamento; HMAC do corpo exato; recusa de redirecionamento de webhook; autenticação, origem e Host; validação HTTP; persistência da pausa; preservação do contexto de uma conexão bem-sucedida; cancelamento do dial em falha; configuração do retry store e pausa após conflito de sessão.

## Testes necessários com o seu celular/VPS

1. Fazer pareamento completo por QR, desconectar conscientemente o vínculo e repetir por código de telefone. O formulário por código foi verificado, mas não houve pareamento desse tipo com um número real fornecido pelo usuário.
2. Enviar/receber texto e cada formato de mídia suportado, com consentimento do contato. Verificar codec, entrega e leitura conforme as configurações do destinatário.
3. Reiniciar container e VPS com uma sessão real; confirmar retomada sem novo QR e sem repetição de pedidos HTTP idempotentes.
4. Interromper a rede em ambiente de teste e observar reconexão, fila e estados incertos.
5. Exercitar mensagens a destinatários com mapeamento PN/LID, pedidos de retry e cenário de reinício entre envio e retry. A implementação usa os mecanismos nativos, mas esses fluxos não foram reproduzidos ponta a ponta com aparelhos reais aqui.
6. Testar receptor HTTPS real: assinatura, timeout, deduplicação, eventos fora de ordem e reprocessamento de dead letters.
7. Executar build Docker, healthcheck, permissões do volume, emissão TLS, backup cifrado e restauração em ambiente isolado, com a instância original parada.
8. Rodar CI Linux com race detector e govulncheck. Tratar resultados antes de adotar para uso contínuo.
9. Observar vários dias de uso real controlado antes de definir uma meta de disponibilidade.

Não houve benchmark comparativo contra Evolution/Uazapi, teste prolongado, teste de banimento, auditoria independente ou garantia de ausência do aviso “aguardando mensagem”. A entrega é código funcional com verificações locais e um roteiro de homologação, não uma certificação de produção.
