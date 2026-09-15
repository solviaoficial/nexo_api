# Deploy no EasyPanel

Use um serviço **App**. O proxy e o HTTPS ficam a cargo do EasyPanel; o container Caddy do `compose.yaml` não é necessário nesse modo.

## 1. Criar o serviço

1. Crie ou abra um projeto no EasyPanel.
2. Clique em **New Service**, escolha **App** e use o nome `nexo-whatsapp`.
3. Em **Source**, escolha **GitHub** e configure:
   - Repository: `solviaoficial/nexo_api`
   - Branch: `main`
   - Build Path: `/`
4. Em **Build**, escolha **Dockerfile** e informe `Dockerfile`.

O repositório é público e não precisa de token do GitHub. Se ele for tornado privado, conecte a conta GitHub ao EasyPanel ou use uma deploy key somente leitura.

## 2. Criar os segredos

Gere os dois valores em um terminal confiável. Cada comando deve ser executado uma vez:

```bash
openssl rand -hex 32
openssl rand -base64 32
```

O primeiro resultado será `ADMIN_TOKEN`. O segundo será `DATA_KEY`. Guarde ambos em um gerenciador de senhas. A `DATA_KEY` deve permanecer igual durante toda a vida do banco.

## 3. Configurar o ambiente

Em **Environment**, informe:

```dotenv
PUBLIC_ORIGIN=https://$(PRIMARY_DOMAIN)
LISTEN_ADDR=0.0.0.0:8080
DATA_DIR=/data
ADMIN_TOKEN=COLE_O_PRIMEIRO_SEGREDO
DATA_KEY=COLE_O_SEGUNDO_SEGREDO
SEND_INTERVAL_SECONDS=10
QUEUE_TTL_HOURS=24
RETENTION_DAYS=7
WEBHOOK_URL=
WEBHOOK_SECRET=
```

Não habilite **Create .env file**: as variáveis podem ser entregues diretamente ao container.

## 4. Persistir a sessão

Em **Storage**, adicione um mount do tipo **Volume**:

- Name/Source: `nexo-data`
- Mount Path: `/data`

Esse volume guarda o banco, a sessão vinculada e a fila. Não remova ou substitua o volume durante atualizações.

## 5. Publicar o domínio

Em **Domains**, adicione seu host, por exemplo `whatsapp.seudominio.com`, com:

- Protocol interno: `HTTP`
- Target port: `8080`
- HTTPS/certificate: habilitado
- Primary domain: habilitado

O registro DNS A do host deve apontar para o IP da VPS. Não publique a porta 8080 em **Ports**.

## 6. Ajustar e implantar

Em **Deploy settings**:

- Replicas: `1`
- Zero-downtime deployment: desabilitado

Uma única instância deve usar a sessão e o volume. Clique em **Deploy**. Ao terminar, confira os logs e abra `https://SEU_DOMINIO/healthz`; a resposta esperada é `{"status":"alive"}`.

Abra o domínio principal, entre com `ADMIN_TOKEN` e faça o pareamento. Primeiro teste envio, recebimento e reinício com um número e um contato de homologação.

## Atualizações

Depois de um novo push na branch `main`, use **Deploy** novamente. Preserve as variáveis e o mount `/data`. Ative Auto Deploy somente depois da homologação, pois cada publicação reinicia o processo e a conexão do WhatsApp.
