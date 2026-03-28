# M4Connect — Guia de Instalação

Conecte seu PC Windows diretamente ao Mac Mini M4 via cabo Ethernet (Cat5e, Cat6, Cat7 ou Cat8).

---

## O que você vai precisar

- Um cabo Ethernet direto entre o PC e o Mac Mini
- Os dois arquivos recebidos:
  - `M4Server.pkg` → instalar no **Mac Mini**
  - `M4Connect.exe` → rodar no **Windows**

---

## 1. Instalar no Mac Mini

Abra o Terminal no Mac e rode os dois comandos abaixo:

```bash
xattr -rd com.apple.quarantine ~/Downloads/M4Server.pkg
sudo installer -pkg ~/Downloads/M4Server.pkg -target /
```

> O segundo comando vai pedir a senha do Mac.

Após instalar, pegue o token de autenticação:

```bash
cat "/Library/Application Support/M4Server/config.json"
```

Anote o valor do campo `"token"` — você vai precisar dele no próximo passo.

---

## 2. Instalar no Windows

1. Clique com o botão direito em `M4Connect.exe` → **Executar como administrador**
2. Se o Windows alertar sobre segurança: clique em **Mais informações** → **Executar assim mesmo**
3. O app vai abrir. Clique na engrenagem (⚙) no canto superior direito
4. Cole o token copiado do Mac no campo **Server token**
5. Clique em **Save**

---

## 3. Conectar

1. Pluga o cabo Ethernet diretamente do PC ao Mac Mini
2. O M4Connect detecta o cabo automaticamente e inicia a conexão
3. Em alguns segundos aparece o painel com CPU, memória e temperatura do Mac

---

## Dúvidas

- **Conexão não inicia** → verifique se está rodando como Administrador
- **Token inválido** → copie novamente o token do Mac e cole nas configurações
- **Mac bloqueia a instalação** → rode os dois comandos do passo 1 na ordem correta

---

*M4Connect — conexão P2P direta, sem internet, sem VPN.*
