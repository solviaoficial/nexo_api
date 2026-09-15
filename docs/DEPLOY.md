# GitHub e VPS

Para instalação pelo EasyPanel, use o guia dedicado em [EASYPANEL.md](EASYPANEL.md).

## 1. Preparar o repositório

Extraia o ZIP e abra um terminal **dentro da pasta nexo-whatsapp**. Não inicialize Git na pasta pai que contém outros projetos.

```bash
git init -b main
git add .
git status
```

Confira se `.env`, `data/`, arquivos de sessão e backups estão ausentes da lista. O ZIP entregue não contém credenciais nem sessão de WhatsApp. Depois:

```bash
git commit -m "Implementa Nexo WhatsApp pessoal"
```

Crie um repositório **privado e vazio** na sua conta do GitHub, sem gerar README adicional. Copie a URL SSH dele:

```bash
git remote add origin git@github.com:SEU_USUARIO/SEU_REPOSITORIO.git
git push -u origin main
```

Confira o resultado em Actions. Não houve publicação automática: a conta e o repositório são seus e ainda precisam ser escolhidos.

## 2. Preparar a VPS

Use Linux 64-bit, armazenamento local SSD e relógio sincronizado por NTP. Instale Docker Engine e o plugin Compose seguindo [a documentação oficial do Docker](https://docs.docker.com/engine/install/ubuntu/). Os comandos abaixo pressupõem que seu usuário tem acesso ao Docker. Esse acesso equivale a administração da máquina.

Confirme:

```bash
docker version
docker compose version
```

Crie um registro DNS A para `whatsapp.seudominio.com`, apontando para o IP da VPS. Se houver AAAA, ele também precisa apontar corretamente. Disponibilize TCP 80 e 443; UDP 443 é opcional para HTTP/3. Preserve sua porta SSH no firewall. Não exponha a porta 8080: o Compose só a disponibiliza na rede interna.

Clone o repositório em um diretório dedicado, por exemplo `/opt/nexo-whatsapp`, ou um diretório pertencente ao seu usuário. Nunca clone por cima de uma aplicação existente.

```bash
git clone git@github.com:SEU_USUARIO/SEU_REPOSITORIO.git nexo-whatsapp
cd nexo-whatsapp
bash scripts/setup.sh
nano .env
```

Configure DOMAIN e PUBLIC_ORIGIN com o mesmo host; a origem inclui `https://` e não tem barra final. `.env` é gerado uma única vez, com permissão restrita. `setup.sh` não sobrescreve um arquivo existente. O diretório data recebe UID/GID 10001 para o usuário do container. Não execute `chmod 777`.

```dotenv
DOMAIN=whatsapp.seudominio.com
PUBLIC_ORIGIN=https://whatsapp.seudominio.com
```

O build testa o código antes de produzir a imagem. Em uma VPS com pouca memória, compile em outra máquina ou configure recursos suficientes. Não substitua o arquivo go.mod por versões “latest”.

```bash
docker compose config --quiet
docker compose up -d
docker compose ps
docker compose logs --tail=100 app
docker compose logs --tail=100 caddy
```

Se o HTTPS falhar, verifique DNS, portas e logs do Caddy. Não ignore avisos de certificado do navegador. Abra o domínio, consulte ADMIN_TOKEN no seu `.env` privado e entre no painel.

## 3. Parear

**QR Code:** clique em Gerar QR Code e, no celular, WhatsApp → Dispositivos conectados → Conectar dispositivo. Escaneie enquanto o código estiver válido. A atualização dos códigos é automática enquanto o pareamento está ativo.

**Código:** selecione Código de pareamento, informe seu número em formato internacional, somente dígitos, e gere o código. No celular, abra Dispositivos conectados → Conectar dispositivo → Conectar com número de telefone. Digite o código apresentado. O texto exato do menu pode variar conforme a versão do aplicativo. Esse código não é um SMS nem transfere a titularidade da conta.

Não vincule simultaneamente esta instalação e outra que use uma cópia da mesma sessão. O primeiro pareamento é novo; não importe diretamente arquivos de sessão da Evolution ou da Uazapi. Mantenha sua implantação anterior parada quando migrar o fluxo de automação, para evitar que as duas aplicações enviem as mesmas notificações.

## 4. Homologar antes do uso principal

Execute os cenários de [validação](VALIDACAO.md) com seu número de teste. Comece com uma mensagem para um contato que consentiu no teste, verifique entrega, resposta e reinício do serviço. Só então conecte sua automação principal. Não há testes de tráfego real associados à sua conta nesta entrega.

## 5. Atualizar

1. Faça backup e confirme o funcionamento da última versão.
2. Pare o app, preservando data e `.env`.
3. Atualize o código e compile uma imagem nova.
4. Suba e confirme status, fila e ausência de conflito.

```bash
docker compose stop app
git pull --ff-only
docker compose build app
docker compose up -d
docker compose ps
```

Em falha de build, você pode iniciar a imagem anterior com `docker compose start app`. Guarde a referência da imagem anterior antes de atualizações maiores. Se uma atualização do Whatsmeow migrar o banco, rollback de binário sem verificar o schema pode ser incompatível: restaure o conjunto consistente de banco/imagem somente após avaliar o risco de estado criptográfico antigo. Nunca rode a restauração ao mesmo tempo que a instalação principal.

## Configuração

| Variável | Uso |
|---|---|
| DOMAIN | Host público usado pelo Caddy |
| PUBLIC_ORIGIN | Origem exata do painel e API; HTTPS em produção |
| ADMIN_TOKEN | Segredo administrativo de pelo menos 32 caracteres |
| DATA_KEY | Chave AES com 32 bytes codificados em base64; preserve em backup |
| DATA_DIR | Diretório persistente; Compose fixa `/data` |
| LISTEN_ADDR | Compose fixa `0.0.0.0:8080`; desenvolvimento usa loopback |
| SEND_INTERVAL_SECONDS | Intervalo entre trabalhos, de 1 a 3.600 segundos; padrão 10 |
| QUEUE_TTL_HOURS | Prazo de mensagens na fila, de 1 a 168 horas; padrão 24 |
| RETENTION_DAYS | Limpeza de conteúdo concluído e eventos entregues/locais; padrão 7 |
| WEBHOOK_URL | Receptor HTTPS opcional, definido pelo operador |
| WEBHOOK_SECRET | Segredo HMAC independente; mínimo 32 caracteres |

Mudanças em `.env` exigem `docker compose up -d --force-recreate app`. Não altere DATA_KEY depois que o banco for criado. Não use `docker compose down -v` como procedimento rotineiro de manutenção.
