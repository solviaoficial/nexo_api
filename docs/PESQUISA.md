# Pesquisa: Uazapi, Evolution e “aguardando mensagem”

Consulta realizada em **14 de setembro de 2026**. A conclusão é técnica e limitada às fontes públicas abaixo; não houve acesso à implementação privada, aos incidentes ou aos indicadores operacionais da Uazapi.

## O que se pode afirmar

A [documentação da Uazapi](https://docs.uazapi.com/) apresenta conexão por instância e estados como connected, connecting, disconnected e hibernated; também recomenda WhatsApp Business e admite inconsistências e desconexões. Os [termos da própria Uazapi](https://www.uazapi.com/terms) dizem que o serviço usa bibliotecas abertas, não é autorizado pelo WhatsApp e pode ser afetado por alterações da plataforma. Não garantem disponibilidade contínua e reconhecem o risco de restrição da conta.

Isso **não comprova** qual biblioteca ou quais patches internos explicariam uma vantagem sobre uma instalação específica da Evolution. Não encontrei, nas fontes consultadas, benchmark controlado ou detalhes suficientes para concluir que “Uazapi é sempre mais estável”. O nome Go não prova o motor interno nem a qualidade da operação.

O [repositório oficial da Evolution API](https://github.com/evolution-foundation/evolution-api) documenta integrações WhatsApp. A [documentação oficial do Evolution Go](https://docs.evolutionfoundation.com.br/evolution-go) já descreve uma implementação Go com Whatsmeow. Portanto, reduzir a comparação a “Uazapi usa Go e Evolution usa Node” seria incompleto. Versão, engine, persistência e configuração da sua Evolution precisam ser identificadas para diagnosticar o caso concreto.

## O aviso no WhatsApp

O [WhatsApp Help Center](https://faq.whatsapp.com/3398056720476987/?eea=0&locale=pt_BR) relaciona esse aviso a falha de entrega, reinstalação, versões desatualizadas e mudança do aparelho principal, no contexto da criptografia de ponta a ponta. Recomenda conexão à internet, atualização dos aplicativos e, conforme o caso, reenvio ou novo vínculo. Também informa a necessidade de acessar o telefone principal periodicamente para manter dispositivos vinculados.

**Inferência de engenharia:** em clientes não oficiais, perda de chaves, sessões concorrentes, mapas de identidade desatualizados, armazenamento inconsistente e falhas no tratamento de retry são possíveis fatores adicionais. Essa é uma hipótese operacional, não o diagnóstico comprovado das suas mensagens. Seriam necessários versões, horários, IDs de mensagens, eventos de reconexão e logs saneados da sua instalação. Não é correto atribuir todas as ocorrências ao servidor, à linguagem ou à Evolution sem essa evidência.

## Por que Whatsmeow neste projeto

O [Whatsmeow](https://github.com/tulir/whatsmeow) é um cliente Go para o protocolo de dispositivos vinculados. A biblioteca oferece envio e recebimento, confirmações e tratamento de solicitações de retry quando a descriptografia falha. Sua [referência de API](https://pkg.go.dev/go.mau.fi/whatsmeow) documenta `UseRetryMessageStore`, `EnableDecryptedEventBuffer`, `SynchronousAck` e recuperação de mensagens pelo telefone.

O [código de retry da versão utilizada](https://github.com/tulir/whatsmeow/blob/9120a3fc6159/retry.go) permite recuperar mensagens persistidas e resolver a correspondência entre telefone (PN) e identificador LID. No [SQL store](https://github.com/tulir/whatsmeow/blob/9120a3fc6159/store/sqlstore/store.go), a retenção nativa de mensagens enviadas para retry é de aproximadamente sete dias. Isso fundamenta a escolha dos recursos ativados; não é uma prova de que todos os destinatários conseguirão descriptografar todas as mensagens.

A dependência está fixada em `v0.0.0-20260914123426-9120a3fc6159`, com checksums no go.sum. Não foi criado protocolo criptográfico próprio nem alterada a biblioteca. Como o Whatsmeow não publica uma versão estável semântica para essa revisão, a homologação de atualizações é necessária.

## O que foi feito para reduzir falhas evitáveis

| Mecanismo | Problema tratado | Limite |
|---|---|---|
| Sessão persistente em SQL | Perda de chaves em reinícios | Falha de disco e restauração antiga continuam possíveis |
| Retry store nativo | Reinício esvaziando cache de mensagens para retry | Retenção e regras do protocolo ainda se aplicam |
| Um dono da sessão | Escritas simultâneas e disputa de conexão | Lock só protege o mesmo volume; não cópias em outro host |
| Contexto de conexão ligado ao serviço | WebSocket cancelado ao terminar uma requisição HTTP | Não impede quedas da rede ou do WhatsApp |
| Fila com idempotência | Cliente repetindo o mesmo pedido HTTP | Chaves diferentes significam pedidos diferentes |
| Estado incerto sem repetição automática | Duplicação após timeout ambíguo | Pode exigir intervenção para concluir uma entrega |
| Outbox transacional | Mudança de estado sem evento correspondente | Receptor precisa deduplicar webhooks |
| Confirmações sem regressão | Recibo atrasado sobrescrevendo “lida” | Leitura depende dos recibos enviados pelo destinatário |
| Reconexão progressiva | Loops de reconexão agressivos | Não contorna suspensão/revogação |

## Sobre “quase nenhum risco de queda por conta da Meta”

QR Code e código por telefone são formas de parear um dispositivo. Usar o segundo não transforma a integração em oficial. Nenhum intervalo, proxy, biblioteca ou nome de navegador estabelece uma taxa comprovada de “não bloqueio”. A Meta pode mudar o protocolo ou restringir a conta. Não foram implementadas técnicas para contornar essas decisões.

Se o requisito futuro passar a ser reduzir a dependência do protocolo do WhatsApp Web, será necessário avaliar a plataforma oficial. O escopo deste projeto segue sua escolha expressa por dispositivo vinculado. Não oferece garantia de disponibilidade nem afirma eliminar o aviso de descriptografia.
