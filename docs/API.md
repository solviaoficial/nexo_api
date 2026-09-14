# API v1 do Nexo

Base: `https://whatsapp.seudominio.com`. Todas as rotas `/api/` exigem `Authorization: Bearer ADMIN_TOKEN`. POST com corpo exige `Content-Type: application/json`. A API aceita até 300 requisições autenticadas por minuto por instalação, incluindo o painel; exceder retorna 429 com Retry-After. Isso é proteção de carga, não limite do WhatsApp.

Erros: `{ "error": "descrição" }`. 400 = formato inválido; 401 = autenticação; 403 = origem; 409 = conflito de chave/estado; 421 = Host diferente de PUBLIC_ORIGIN; 429 = limite; 503 = persistência/serviço indisponível. Campos JSON desconhecidos são rejeitados.

## Conexão e fila

| Método e rota | Corpo / resposta |
|---|---|
| GET /api/status | Estado da conexão, QR/code temporários, pausa da fila, intervalo e webhook |
| POST /api/connection/connect | `{}` para QR/retomada; `{"phone":"5511999999999"}` para pareamento por número |
| POST /api/connection/pause | `{}`; interrompe a conexão sem apagar credenciais |
| POST /api/queue | `{"paused":true}` ou `{"paused":false}`; persistido |
| GET /api/metrics | Contagem de mensagens por estado |
| GET /healthz | Liveness, sem autenticação; não representa conexão ao WhatsApp |
| GET /readyz | Banco e heartbeat do worker, sem autenticação; 503 em falha |

A pausa não desfaz uma mensagem que já estava sendo transmitida. Um pareamento ativo não é substituído por outra solicitação: aguarde expirar ou pause a conexão. Após revogação, reinicie o app antes de um novo vínculo, pois a biblioteca pode marcar o objeto da sessão antiga como excluído. Retomar uma conta ainda vinculada reutiliza a sessão salva.

## Enviar

`POST /api/messages` exige também **Idempotency-Key** (8–128 caracteres: letras, números, `_ . : -`).

```json
{"to":"5511999999999","kind":"text","text":"Olá!"}
```

`to` é número com DDI, sem `+`, espaços ou JID. Não há envio a grupos nesta API. Texto tem limite de 4.096 caracteres.

Para mídia, `data` é base64 puro, sem prefixo `data:`, até 16 MiB após decodificação. Exemplo estrutural (substitua o conteúdo):

```json
{
  "to":"5511999999999",
  "kind":"document",
  "mime":"application/pdf",
  "filename":"documento.pdf",
  "text":"Segue o documento.",
  "data":"BASE64_DO_ARQUIVO"
}
```

| kind | MIME aceitos |
|---|---|
| text | Sem MIME/data |
| image | image/jpeg, image/png |
| document | application/pdf, text/plain |
| video | video/mp4 |
| audio | audio/mpeg, audio/ogg, audio/mp4 |

O sistema não transcodifica nem valida todos os codecs internos. Áudio é enviado como arquivo de áudio normal, não como gravação PTT. Texto em áudio não vira legenda. Uma extensão válida não garante que o arquivo esteja íntegro; a preparação ou entrega pode falhar.

Retorno inicial HTTP 202, com `{id,to,kind,status,created,updated,expires}`. Todos os horários são Unix em segundos. Repetir mesma chave/conteúdo retorna HTTP 200 e o registro original; mesmo se já enviado, não cria novo envio. Mesma chave com conteúdo diferente: 409.

| Método e rota | Uso |
|---|---|
| GET /api/messages | Últimas 100 mensagens; metadados sem corpo/arquivo |
| GET /api/messages/{id} | Estado de um envio |
| POST /api/messages/{id}/cancel | Cancela somente se ainda queued |

`uncertain` significa que pode ter sido enviado. Confira no telefone/recibos antes de fazer um novo POST com uma nova chave. Não há botão de reenvio automático para esse caso. Uma chamada repetida com a chave original é sempre consulta do mesmo pedido, não tentativa de repetir a transmissão.

## Eventos e webhooks

`GET /api/events?after=0` retorna até 100 eventos em ordem crescente de `seq`. Salve o último seq e use-o em `after`. Um array vazio indica que não há eventos novos. Retenção pode criar lacunas na sequência: não assuma continuidade numérica.

```json
{
  "seq":17,
  "id":"id-global-do-evento",
  "type":"message.received",
  "created":1789398000,
  "data":{"id":"id-whatsapp","chat":"jid","sender":"jid","text":"Olá","kind":"text","timestamp":1789398000},
  "delivery":"pending",
  "attempts":0
}
```

Os identificadores recebidos podem ser PN, LID ou grupo; não retire o sufixo presumindo que todo JID contém um telefone. Eventos de mídia recebida incluem tipo e legenda quando disponíveis, não o arquivo. Eventos de controle do protocolo e mensagens do próprio remetente são ignorados pelo fluxo de recebimento.

Tipos principais: message.queued/sending/sent/delivered/read/failed/uncertain/cancelled, message.received, message.undecryptable, connection.connected/logged_out/conflict/outdated/failure.

Se configurado, o webhook recebe o mesmo formato por POST, com:

```text
X-Nexo-Event-ID: <id>
X-Nexo-Timestamp: <Unix em segundos>
X-Nexo-Signature: sha256=<HMAC-SHA256 em hexadecimal>
```

Assinatura: `HMAC_SHA256(WEBHOOK_SECRET, timestamp + "." + raw_body)`. Valide os **bytes originais**, antes de fazer parse/reformatar o JSON. Rejeite timestamps antigos (> 5 minutos), compare em tempo constante e grave o event ID no banco do receptor para deduplicar. Devolva 2xx depois de persistir no seu sistema. Não há garantia de ordem de entrega por webhook.

Após 10 tentativas malsucedidas: `delivery=dead`. `POST /api/events/{id}/retry` com `{}` reinicia tentativas somente para eventos dead e webhook configurado. O painel permite essa ação nos eventos apresentados. Essa ação pode repetir um evento que o receptor já processou antes de um timeout, daí a necessidade de deduplicação.
