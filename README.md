# Nexo · WhatsApp pessoal

API e painel próprios para conectar **um número por instalação**, usando **QR Code ou código de pareamento pelo telefone**. Backend Go + Whatsmeow, armazenamento SQLite persistente, Docker Compose e HTTPS com Caddy.

Este projeto é uma implementação nova para uso pessoal, com mecanismos de confiabilidade e testes automatizados. **Não é possível garantir 100% de disponibilidade, ausência de “aguardando mensagem” ou proteção contra restrições da Meta em uma conexão não oficial.** Não há benchmark que demonstre que este projeto supera Uazapi ou Evolution. Leia [a pesquisa e as decisões técnicas](docs/PESQUISA.md) e [o relatório de validação](docs/VALIDACAO.md).

## O que está implementado

- QR Code e código de pareamento, mostrados no painel autenticado.
- Retomada da sessão salva, reconexão com espera progressiva e variação aleatória, pausa persistente e bloqueio quando há conflito ou revogação.
- Chaves, sessões Signal, mapas PN/LID e armazenamento nativo de mensagens para retry persistidos pelo Whatsmeow.
- Fila durável: SQLite WAL + synchronous FULL, chave de idempotência obrigatória, expiração e limite de 1.000 mensagens pendentes.
- Uma mensagem por vez, intervalo configurável; o padrão é 10 segundos, uma escolha de carga, **não uma garantia contra bloqueio**.
- Envio de texto, imagens JPEG/PNG, PDF/TXT, vídeo MP4 e áudio MP3/OGG/M4A. Limite de 16 MiB por arquivo. O codec também precisa ser aceito pelo WhatsApp; não há transcodificação.
- Confirmações de envio, entrega e leitura, quando recebidas. `sent` significa confirmação do servidor, não entrega ao destinatário.
- Recebimento de texto e metadados de mídia por eventos. Não inclui download automático de mídia recebida ou sincronização de histórico antigo.
- Webhooks opcionais assinados, tentativas com espera progressiva e fila de falhas; consulta de eventos com cursor também funciona sem webhook.
- Token administrativo, proteção de origem/Host, cabeçalhos de segurança, payloads de aplicação cifrados e bloqueio de instâncias duplicadas no mesmo volume.
- Docker sem root para a aplicação, healthchecks, limites de recursos, logs limitados e procedimento de backup criptografado.

Não é um clone com compatibilidade de rotas da Uazapi/Evolution. Não inclui multiatendimento, campanhas, grupos, chatbot, proxy de evasão, rotação de contas ou mecanismos “anti-ban”. Para vários números, use instalações e volumes separados.

## Começar na VPS

Se você usa EasyPanel, siga o [guia específico do EasyPanel](docs/EASYPANEL.md). Para instalação direta com Docker Compose, continue abaixo.

Recomendação inicial: Linux com **2 vCPU, 2 GB de RAM e 20 GB SSD**, um subdomínio e Docker Engine + Compose plugin. É um ponto inicial para baixo volume, não capacidade comprovada em carga. O build pode exigir mais RAM que a execução; se necessário, use uma máquina de build.

1. Suba esta pasta em um repositório privado do GitHub. Veja [GitHub e deploy](docs/DEPLOY.md).
2. Na VPS, clone o repositório e execute:

```bash
bash scripts/setup.sh
nano .env
```

3. Em `.env`, altere somente estes campos inicialmente:

```dotenv
DOMAIN=whatsapp.seudominio.com
PUBLIC_ORIGIN=https://whatsapp.seudominio.com
```

O script gera ADMIN_TOKEN, DATA_KEY e WEBHOOK_SECRET aleatórios. Não substitua esses valores pelos exemplos de testes. Não publique `.env`.

4. Aponte o DNS para a VPS, libere 80/443 e inicie:

```bash
docker compose up -d
docker compose ps
docker compose logs --tail=100 app
```

5. Abra seu domínio, entre com ADMIN_TOKEN e conecte o celular pelo método desejado. Use uma conta de teste na homologação antes de depender do serviço.

## Integração por API

```bash
# Defina TOKEN no terminal de forma privada; não grave seu token neste arquivo.
read -r -s -p 'ADMIN_TOKEN: ' TOKEN; echo
curl --fail-with-body 'https://whatsapp.seudominio.com/api/messages' \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: meu-pedido-000001' \
  --data '{"to":"5511999999999","kind":"text","text":"Olá!"}'
unset TOKEN
```

Troque o destinatário por um contato seu. Este comando **envia uma mensagem** quando a conexão estiver ativa. Em falha de rede na chamada HTTP, repita **a mesma chave com o mesmo conteúdo**. Alterar a chave cria outro envio.

[Referência completa da API](docs/API.md) · [Arquitetura](docs/ARQUITETURA.md) · [Operação e recuperação](docs/OPERACAO.md) · [Segurança](SECURITY.md)

## Desenvolvimento

Go **1.27.1**, sem dependência de compilador C para builds normais:

```bash
go mod download
go test ./...
go vet ./...
go build -o nexo .
./nexo init-env
```

O binário não lê `.env` automaticamente: use Docker Compose ou carregue suas variáveis no ambiente antes de executá-lo. No Linux, para um `.env` gerado por você e confiável: `set -a; . ./.env; set +a; ./nexo`. Mantenha PUBLIC_ORIGIN=http://localhost:8080 e LISTEN_ADDR=127.0.0.1:8080 para desenvolvimento local. A CI também executa `go test -race` em Linux e o build Docker.

## Limite de confiabilidade

A fila fica no disco da sua VPS. Uma única VPS não oferece alta disponibilidade durante falha de host/disco/rede. Um timeout de envio pode ocorrer depois que a mensagem foi aceita pelo WhatsApp; nesses casos o Nexo marca `uncertain` e aguarda verificação humana. Não há promessa de exactly-once entre dois sistemas que não compartilham uma transação.

Mantenha o WhatsApp atualizado, acompanhe os eventos e valide atualizações do Whatsmeow antes de promovê-las. Uma restauração antiga das sessões pode também restaurar estado criptográfico desatualizado. Não apague bancos nem gere novas sessões como tratamento automático de incidentes.
