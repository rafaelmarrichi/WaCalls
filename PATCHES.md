# Patches da Malamute

Fork de [JotaDev66/WaCalls](https://github.com/JotaDev66/WaCalls), branch `develop`.
Base: `d16a076` (27 de julho de 2026).

Este arquivo é o mapa de reaplicação dos patches. Leia a seção seguinte antes de tentar rebase.

## Por que não dá rebase

A branch `develop` do upstream é um **commit órfão**: um único commit, sem pai, sem histórico. Não
existe merge-base com a `main`, que por sua vez parou na v1.0.0 em 25 de junho e tem 19 commits que a
`develop` não conhece. O upstream esmaga a branch num snapshot a cada publicação.

Consequência prática: quando sair uma `develop` nova, ela virá como outra raiz órfã e o
`git rebase` não terá ancestral comum para trabalhar. A reaplicação é por `git format-patch` e
`git am`, e este arquivo existe para quando o `am` falhar e for preciso refazer à mão.

```sh
# gerar os patches do nosso trabalho
git format-patch <base>..malamute/fase-3 -o /tmp/malamute-patches

# depois de trocar a base pelo snapshot novo
git am /tmp/malamute-patches/*.patch
```

## Princípio: o conflito fica nos arquivos novos

O trabalho está concentrado em **arquivos que só existem no fork**, que por definição nunca conflitam.
Os arquivos do upstream recebem o mínimo, e cada toque está listado abaixo com o motivo.

### Arquivos novos (não conflitam)

| Arquivo                                | O que é                                          |
| -------------------------------------- | ------------------------------------------------ |
| `internal/app/record/recorder.go`      | gravador estéreo com relógio próprio             |
| `internal/app/record/ring.go`          | buffer circular que nunca bloqueia a mídia       |
| `internal/app/record/wav.go`           | escrita do WAV canônico                          |
| `internal/app/player/player.go`        | injeção de áudio de aviso                        |
| `internal/app/player/library.go`       | cache e armazenamento dos assets                 |
| `internal/app/player/wav.go`           | leitura de WAV mono 16 kHz                       |
| `internal/app/session/recording.go`    | o funil de saída, ciclo de vida por chamada      |
| `internal/app/events/recording.go`     | evento `call.recording` e `call-announce-done`   |
| `internal/app/handlers_record.go`      | endpoints de aviso, gravação e upload de asset   |
| `internal/app/config/malamute.go`      | leitura e padrões das variáveis novas            |
| `internal/voip/call/observer_access.go`| getter do observer por chamada                   |
| `cmd/server/deviceprops.go`            | nome do dispositivo e timeout de toque           |

Mais os testes correspondentes, que também são arquivos novos.

### Arquivos do upstream tocados

| Arquivo                            | Alteração                                                           |
| ---------------------------------- | ------------------------------------------------------------------- |
| `internal/app/config/config.go`    | 6 campos no `Config` e 6 linhas no literal do `LoadConfig`          |
| `internal/app/server.go`           | monta o `AudioConfig`, passa ao manager e guarda no `Server`         |
| `internal/app/routes.go`           | 6 rotas novas na tabela, e `PUT` na lista de métodos do CORS         |
| `internal/app/openapi.yaml`        | documenta as 6 rotas (há teste que exige isso)                       |
| `internal/app/session/manager.go`  | campo `audioCfg`, campo `Audio` no `Deps`, atribuição no construtor  |
| `internal/app/session/session.go`  | campo `audio`, e 4 pontos de fiação                                 |
| `internal/app/session/commands.go` | `OnBrowserPCM` passa a entrar pelo funil                            |
| `internal/app/events/webhook.go`   | 1 campo `Recording` no `webhookEvent`, ponteiro com `omitempty`      |

Nada de autenticação, multi-tenancy ou lógica de campanha entrou no Go. Isso vive na API TypeScript.

---

## Patch 1: gravação de chamada

Um WAV estéreo por chamada atendida. Canal esquerdo o que enviamos, canal direito o que o contato
enviou. PCM 16 bits little endian, 2 canais, 16 kHz.

**O problema do relógio.** Os dois lados chegam em ritmos independentes. O contato chega com os
pacotes RTP, em quadros de 60 ms, com perda e ocultamento. O nosso lado chega quando o navegador
manda, e **para por completo quando não há navegador conectado**, que é o estado normal no começo de
toda chamada da discadora. Intercalar os dois buffers conforme chegam acumula dessincronia.

Por isso o gravador tem relógio próprio: uma goroutine acorda a cada 20 ms e escreve **sempre** um
quadro, tirando 320 amostras de cada lado e completando com silêncio o que faltar.

**Duas decisões que divergem do desenho original**, ambas por defeito encontrado escrevendo o código:

1. **A contagem de quadros vem do tempo decorrido, não do número de ticks recebidos.** Um `Ticker` do
   Go descarta ticks quando a goroutine está sufocada, e ticks descartados encurtariam o arquivo em
   silêncio. Derivar do relógio de parede mantém o alinhamento. A recuperação é limitada a 50 quadros
   por acordada, para uma pausa longa não virar rajada de escrita.

2. **O fechamento esvazia o buffer em vez de descartar.** Na prática é um quadro ou dois, porque o
   relógio acompanha a chegada. Importa quando o relógio atrasou: aquele áudio é real, é o fim da
   conversa, e parar o relógio sem escrever jogaria fora justamente a parte que mais importa. O custo
   é o arquivo poder passar um pouco do tempo de parede.

**Proteções.** Buffer circular com teto (padrão 2 s por lado) que descarta o mais antigo e conta a
perda, nunca bloqueia o caminho de mídia. Escrita por `bufio.Writer` de 64 KB com flush a cada
segundo. As amostras são copiadas com o mutex do gravador e a escrita em disco acontece fora dele. A
goroutine é registrada no observer da chamada, então os testes de vazamento do upstream continuam
valendo.

**Cabeçalho.** Os dois campos de tamanho só são conhecidos no fim, então saem zerados e são
reescritos no fechamento com dois `WriteAt`. Uma gravação interrompida por queda do processo fica com
cabeçalho zerado: ainda decodificável por ferramenta tolerante, e detectável por nós porque os
tamanhos não batem com o arquivo em disco.

**Quando começa.** No primeiro estado conectado, não na discagem: chamada não atendida não tem o que
gravar e deixaria um arquivo vazio por tentativa. É idempotente, porque o estado conectado é
reportado de novo depois de reconexão de mídia.

Caminho: `${WACALLS_RECORD_DIR}/${sessionID}/${callID}.wav`.

## Patch 1b: webhook `call.recording`

O despachante do upstream só carregava `CallRecord`, então o `webhookEvent` ganhou um campo
`Recording`, ponteiro com `omitempty`, para os três eventos existentes manterem o payload byte a
byte.

**Ordem de entrega, que importa para quem consome:** o `call.recording` sai **antes** do
`call.ended`. O gravador é fechado como parte do teardown, e o arquivo precisa ser anunciado
enquanto o registro da chamada ainda existe, porque o `EndCall` do broker apaga o registro. Quem
consome não pode assumir que a chamada já está marcada como encerrada quando este evento chega.

O `sha256` do payload é o do **WAV como escrito aqui**, para quem baixa distinguir transferência
truncada de completa. Não é o hash do arquivo final armazenado, que é outro formato.

## Patch 2: injeção de áudio (aviso de gravação)

Sem isso o contato atende e ouve silêncio absoluto até o atendente entrar, e desliga. E o aviso de
gravação é exigência legal.

**A correção mais importante deste patch está no desenho, não no código.** O desenho original mandava
gravar o canal do atendente a partir do `OnBrowserPCM`, o áudio do microfone. Mas o aviso é injetado
pelo `FeedCapturedPCM`, que é outro caminho. Seguindo aquilo à risca, **o aviso não apareceria na
gravação**, e é justamente a gravação que prova que o aviso tocou.

Então tudo que vai para o contato passa por **um funil único**, `feedOutbound` em
`internal/app/session/recording.go`: o microfone do atendente e o aviso. O canal esquerdo passa a ser
"o que enviamos" e o direito "o que recebemos".

O funil também resolve a segunda regra: enquanto o aviso toca, o microfone é **descartado**, não
misturado. Atendente já conectado e falando seria ouvido por cima do aviso legal.

**Ritmo.** As amostras são empurradas a 320 por 20 ms, não despejadas de uma vez. A extensão de áudio
mantém um buffer de captura pequeno e descarta o excesso, então entregar cinco segundos de uma vez
tocaria só o rabo do arquivo.

**Formato dos assets.** Mono, 16 kHz, 16 bits PCM. Outro formato é **recusado, não reamostrado**:
taxa errada toca na velocidade e no tom errados, e um contato ouvindo o aviso legal em voz de
esquilo é pior que uma falha limpa. O leitor percorre a lista de chunks em vez de assumir cabeçalho
de 44 bytes, porque arquivo exportado por ferramenta de áudio comum traz `LIST` ou `fact` antes do
`data` e decodificaria como ruído.

**Barge-in não existe.** O endpoint aceita `interruptible` e **recusa** `true`, em vez de aceitar e
tocar sem interrupção de qualquer jeito. O áudio que este endpoint existe para tocar é obrigatório
por lei, então ignorar em silêncio o que o chamador pediu é o padrão errado.

## Patch 2b: upload de asset (`PUT /api/audio/{name}`)

Este endpoint não estava no desenho original e resolve um furo dele: o desenho dizia que o asset
"fica em `${WACALLS_AUDIO_DIR}`" e parava aí. Com aviso por tenant e o arquivo vivendo em
armazenamento de objeto, faltava a ponte.

A API empurra o WAV para cá uma vez, quando o tenant faz o upload. Três motivos para ser assim, e não
volume compartilhado nem download na hora de tocar:

- funciona com várias instâncias, porque a API empurra para cada uma;
- o motor nunca precisa de credencial de armazenamento;
- mantém a rede fora do caminho crítico da chamada. Um aviso que precisa ser baixado quando o contato
  atende é um aviso que falha quando o S3 está lento, e aviso falho derruba a chamada.

O nome do asset chega pela URL, então é validado contra `^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$` antes de
chegar perto do sistema de arquivos. A escrita vai para arquivo temporário e é renomeada, para uma
chamada que carrega o asset durante um upload nunca ver arquivo pela metade. O nome do asset precisa
levar o `tenantId` dentro, senão um tenant toca o aviso do outro.

Requisições são limitadas a 1 MiB pelo middleware do upstream, cerca de 32 segundos de áudio, folga
larga para um aviso de 4 a 6 segundos.

## Patch 4: ajustes menores

**Timeout de toque configurável.** `WACALLS_RING_TIMEOUT_SEC`, zero ou ausente mantém os 60 s do
upstream. O `call.DefaultTimeouts` é variável de pacote copiada para cada `CallManager` na
construção, então atribuir a ela no boot alcança toda chamada futura e nenhuma existente. No boot não
existe nenhuma, e é por isso que o lugar é o `cmd/server`.

**Nome do dispositivo pareado.** Confirmado em campo na Fase 1: o upstream nunca toca em
`store.DeviceProps`, cujo padrão no whatsmeow é `Os: proto.String("whatsmeow")`. O chip aparece na
lista de aparelhos conectados do celular do cliente com o nome da biblioteca.

A decisão foi nome próprio com ícone de navegador, e não imitar um navegador: cliente que não
reconhece uma entrada revoga, e revogar desconecta o chip e para a campanha.

Duas limitações que precisam estar claras para quem operar:

- o nome é **por instância**, porque `store.DeviceProps` é global do processo. Nome por cliente exige
  instância dedicada.
- o nome só vale para **pareamento novo**, porque é gravado no momento de vincular o aparelho. Chip
  já conectado continua como está até reparear.

**Não mexemos** no `-max-calls-per-session`, que segue global como teto rígido de segurança. O
controle por chip fica na nossa API, que é quem conhece tenant e limite.

---

## Variáveis de ambiente

Todas inertes quando não setadas: sem elas o binário se comporta como o upstream. Há teste que
garante isso (`internal/app/config/malamute_test.go`).

| Variável                  | Padrão              | Efeito                                            |
| ------------------------- | ------------------- | ------------------------------------------------- |
| `WACALLS_RECORD_DIR`      | vazio               | vazio desliga a gravação por completo             |
| `WACALLS_RECORD_MAX_MB`   | `200`               | teto por arquivo, encerra a gravação e loga       |
| `WACALLS_AUDIO_DIR`       | vazio               | vazio desliga os avisos por completo              |
| `WACALLS_RING_TIMEOUT_SEC`| vazio               | vazio mantém os 60 s do upstream                  |
| `WACALLS_DEVICE_NAME`     | `Malamute Discador` | texto na lista de aparelhos do cliente            |
| `WACALLS_DEVICE_PLATFORM` | `chrome`            | só o ícone, sem efeito no nome                    |

## Endpoints novos

```
POST   /api/sessions/{sid}/calls/{id}/play        { asset, interruptible: false } → { durationMs }
POST   /api/sessions/{sid}/calls/{id}/stopplay    → 204
GET    /api/sessions/{sid}/calls/{id}/recording   → audio/wav, aceita range
DELETE /api/sessions/{sid}/calls/{id}/recording   → 204, idempotente
PUT    /api/audio/{name}                          corpo é o WAV → { durationMs }
DELETE /api/audio/{name}                          → 204, idempotente
```

Todos atrás do mesmo `WACALLS_API_TOKEN` do resto de `/api`, e todos documentados no `openapi.yaml`,
porque o upstream tem teste que recusa rota não documentada.

## Eventos novos

Webhook `call.recording`, e SSE `call-recording` e `call-announce-done`.

O `call-announce-done` é o sinal confiável de que a linha está livre, e não a duração devolvida pelo
`play`, porque a reprodução pode ser cortada. Ele carrega `completed` e `playedMs` justamente para
distinguir aviso completo de aviso interrompido.

## Como rodar os testes

```sh
gofmt -l ./cmd ./internal   # precisa sair vazio
go vet ./...
go test -race ./...
```

O teste de formato do WAV usa `ffprobe` quando ele existe e se marca como pulado quando não, então CI
sem ffmpeg continua passando.
