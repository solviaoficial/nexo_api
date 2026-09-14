# Operação, diagnóstico e recuperação

## Rotina

- Acompanhe conexão, queued, uncertain, failed, eventos undecryptable e webhooks dead. A aba de eventos usa carregamento manual; uma automação pode consultar o cursor ou receber webhooks.
- Monitore externamente `/readyz`, disco da VPS, expiração TLS, reinícios e uso de memória. HTTP 200 nesse endpoint não significa que o número está conectado; monitore também `/api/status` autenticado.
- Execute e copie backups criptografados para fora da VPS, com retenção definida por você. Um backup no mesmo disco não protege contra perda do host.
- Mantenha sistema, aplicativo oficial e dependências atualizados com homologação. Não reinicie o serviço continuamente para tentar corrigir falhas de protocolo.

## Backup

Instale a ferramenta [age](https://github.com/FiloSottile/age) a partir de sua origem oficial. Gere a chave privada em um lugar seguro, preferencialmente fora da VPS. Configure somente a chave pública (recipient) na rotina de backup:

```bash
export AGE_RECIPIENT='age1SUA_CHAVE_PUBLICA'
bash scripts/backup.sh
```

O script para o app, cria um snapshot de data + `.env`, cifra o fluxo e reinicia o app ao sair. A indisponibilidade durante a cópia é deliberada para preservar consistência entre os dois bancos e os WALs. Caddy permanece ativo, mas pode retornar erro enquanto o app está parado. O backup não é agendado automaticamente: configure a periodicidade no seu servidor após testar.

Teste a integridade de um backup real, em máquina segura:

```bash
age -d -i chave-privada.age backups/nexo-DATA.tar.age | tar -tzf -
```

## Restauração

Restaure em diretório novo, vazio e restrito; examine a lista de arquivos do arquivo gerado pelo próprio script. Desligue a implantação antiga **antes** de iniciar a cópia restaurada. Nunca teste uma sessão clonada enquanto a original está ativa.

```bash
mkdir -m 700 restauracao
age -d -i chave-privada.age backups/nexo-DATA.tar.age | tar -xzf - -C restauracao
```

Combine os arquivos restaurados com a mesma versão do código/imagem, preserve `.env` e permissões UID 10001 de data e suba uma única instalação. Não use o setup como forma de gerar outra DATA_KEY para dados antigos. Restaurar uma sessão antiga pode causar descompasso criptográfico; valide antes de retomar envios. Se o WhatsApp exigir novo pareamento, faça-o conscientemente, preservando uma cópia do estado anterior para diagnóstico.

## Incidentes

| Sintoma | Ação |
|---|---|
| Aguardando mensagem | Confira versões do aplicativo, conectividade, troca/reinstalação de telefone e eventos undecryptable. Preserve a sessão. Verifique no telefone principal. O sistema não consegue garantir recuperação retroativa. |
| uncertain | Não repita automaticamente. Compare mensagem e horário no telefone, aguarde recibos e decida se precisa de um novo envio. |
| disconnected | Verifique rede/DNS e logs. O loop tenta reconectar com espera crescente. Não apague o banco. |
| paused por conflito | Localize outro processo ou VPS usando a mesma sessão. Pare a duplicata antes de retomar. |
| logged_out | Verifique Dispositivos conectados no celular. Reinicie o app para descartar o objeto revogado e faça novo pareamento pelo painel. |
| outdated/failure | Verifique motivo/evento, versão Whatsmeow e situação da conta. Atualize com homologação; não force reconexões em loop. |
| readyz 503 ou fila pausada após erro | Verifique espaço/permissão do disco e logs. Corrija a causa e retome a fila; não delete bancos. |
| webhook dead | Confira endpoint, TLS, assinatura e armazenamento do receptor. Reprocesse apenas depois da correção. |
| Host não autorizado | PUBLIC_ORIGIN deve corresponder exatamente ao domínio e porta usados pelo cliente/proxy. |
| Chave incorreta ao iniciar | Restaure a DATA_KEY correspondente; não substitua por uma chave nova. |

## Critérios para uso contínuo

Depois da homologação funcional, observe por vários dias no seu ambiente: número de desconexões, tempo até reconectar, latência da fila, taxa de mensagens sem confirmação, erro de descriptografia e recursos da VPS. Use esses resultados para definir metas. Não transforme testes unitários ou uma sessão curta em uma taxa declarada de disponibilidade anual.

O projeto prioriza controle e rastreabilidade, mas uma solução nova requer manutenção. Se um erro reproduzível aparecer, mantenha ID, horários e versões em um registro saneado. Não compartilhe chaves da sessão nem dados pessoais completos em issues públicas.
