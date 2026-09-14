# Segurança operacional

Não publique `.env`, `data/`, QR Codes, códigos de pareamento ou backups. ADMIN_TOKEN permite todas as operações; este projeto é de administrador único. O painel guarda o token somente em memória e exige novo login após recarregar a página. Guarde o token em um gerenciador de senhas.

Use HTTPS, atualizações do sistema e firewall. Para uso exclusivamente pessoal, restrinja o painel na rede da VPS/VPN; webhooks são enviados para fora e não exigem liberar uma rota de entrada adicional. A API não habilita CORS.

O arquivo `app.db` cifra os corpos de mensagens/eventos com AES-256-GCM. IDs, destinatários, estados e horários são metadados em claro. **`session.db` contém as chaves criptográficas e o armazenamento nativo de retry do Whatsmeow sem criptografia de aplicação.** Proteja o disco da VPS e os backups. Não prometa que todos os dados locais estão cifrados. ADMIN_TOKEN e DATA_KEY estão em `.env`. Não troque DATA_KEY sem uma migração: isso tornará os dados existentes ilegíveis e o serviço recusará iniciar.

Os logs da biblioteca são desativados para evitar o registro acidental de dados do protocolo; eventos operacionais relevantes são registrados pela aplicação. O sistema não registra tokens nem o corpo das mensagens em stdout.

WEBHOOK_URL só pode ser alterada pela configuração do operador. A aplicação exige HTTPS e não segue redirecionamentos. Como a URL é configuração administrativa, destinos privados são permitidos: configure somente o seu receptor. Cada entrega tem HMAC-SHA256 de `timestamp + "." + corpo exato`; valide a assinatura em tempo constante, recuse timestamps com mais de 5 minutos e deduplique X-Nexo-Event-ID.

O Docker executa a aplicação sem root, com filesystem principal somente leitura, capabilities removidas e limites de recursos. Caddy gerencia o TLS. Um arquivo de lock impede dois processos usando **o mesmo volume**; não detecta uma cópia do banco em outra VPS. Nunca rode duas cópias da mesma sessão. Não remova o volume nem a sessão para tratar quedas rotineiras.

Atualize dependências em uma branch e execute CI + homologação com número de teste. Não configure `go get ...@latest` no startup. As versões resolvidas estão em go.mod/go.sum. Para reportar falhas, compartilhe uma reprodução com dados fictícios e versões, nunca arquivos de sessão.
