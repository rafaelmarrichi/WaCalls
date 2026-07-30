package session

import (
	"context"
	"errors"
	"strings"

	"go.mau.fi/whatsmeow/types"
)

// Consulta de números registrados no WhatsApp, adicionada pelo fork.
//
// Discar para número que não tem WhatsApp queima tentativa, sobe a taxa de falha
// do chip e piora o padrão de comportamento da conta aos olhos da Meta. O
// upstream disca sem checar nada; quem importa uma planilha de mil linhas precisa
// saber antes quais delas fazem sentido.
//
// O `IsOnWhatsApp` do whatsmeow é uma consulta USync ao servidor da Meta.
// Consultar em volume também chama atenção, então o espaçamento é
// responsabilidade de quem chama: lotes pequenos, intervalo entre eles e
// distribuição entre os chips do cliente. Este método não impõe ritmo, e o teto
// por requisição fica na borda HTTP.

// ErrNotPaired é devolvido quando a sessão ainda não tem um WhatsApp vinculado.
var ErrNotPaired = errors.New("session is not paired")

// NumeroNoWhatsApp é o resultado por número consultado.
type NumeroNoWhatsApp struct {
	// Phone é o número exatamente como o chamador enviou, para ele conseguir
	// casar a resposta com a linha da planilha dele.
	Phone  string `json:"phone"`
	Exists bool   `json:"exists"`
	// JID canônico, vazio quando o número não está no WhatsApp.
	JID string `json:"jid,omitempty"`
}

// IsOnWhatsApp consulta quais dos números estão registrados no WhatsApp.
//
// Devolve **um resultado por número consultado, na ordem em que vieram**, e isso
// não é detalhe de conveniência. O whatsmeow devolve só os usuários sobre os
// quais o servidor respondeu: número ausente da resposta é indistinguível, para
// quem só olha o tamanho da lista, de número que não existe. Deixar o consumidor
// alinhar por índice seria convidar a um desalinhamento silencioso que colocaria
// o resultado de um contato na linha de outro.
func (s *Session) IsOnWhatsApp(ctx context.Context, phones []string) ([]NumeroNoWhatsApp, error) {
	if !s.IsPaired() {
		return nil, ErrNotPaired
	}

	if len(phones) == 0 {
		return []NumeroNoWhatsApp{}, nil
	}

	// O whatsmeow espera o formato internacional com `+`. Quem chama guarda o
	// telefone sem ele, então a normalização acontece aqui, e não vira regra que
	// cada chamador precisa lembrar.
	consulta := make([]string, len(phones))
	for i, phone := range phones {
		consulta[i] = comMaisNaFrente(phone)
	}

	respostas, err := s.client.IsOnWhatsApp(ctx, consulta)
	if err != nil {
		return nil, err
	}

	return alinharResultados(phones, respostas), nil
}

// alinharResultados casa a resposta da Meta com a lista que foi perguntada.
//
// Função pura e separada porque é aqui que mora o risco desta funcionalidade. A
// resposta do USync traz só os usuários sobre os quais o servidor respondeu, em
// quantidade e ordem que não acompanham a pergunta: alinhar por posição
// colocaria o resultado de um contato na linha de outro, e o importador marcaria
// como "sem WhatsApp" um número que tem, ou o contrário. Nada disso apareceria
// como erro em lugar nenhum.
//
// O casamento é pelo número, e o resultado tem sempre um item por pergunta.
func alinharResultados(phones []string, respostas []types.IsOnWhatsAppResponse) []NumeroNoWhatsApp {
	// Indexa pelo número sem o `+` para casar com o que o chamador mandou. O
	// campo `Query` traz a string que foi consultada, mas a resposta também pode
	// vir identificada só pelo JID, então as duas formas entram no índice.
	encontrados := make(map[string]string, len(respostas))
	for _, resposta := range respostas {
		if !resposta.IsIn {
			continue
		}
		jid := resposta.JID.String()
		if consultado := semMaisNaFrente(resposta.Query); consultado != "" {
			encontrados[consultado] = jid
		}
		if resposta.JID.User != "" {
			encontrados[resposta.JID.User] = jid
		}
	}

	resultados := make([]NumeroNoWhatsApp, len(phones))
	for i, phone := range phones {
		jid, existe := encontrados[semMaisNaFrente(phone)]
		resultados[i] = NumeroNoWhatsApp{Phone: phone, Exists: existe, JID: jid}
	}

	return resultados
}

func comMaisNaFrente(phone string) string {
	limpo := strings.TrimSpace(phone)
	if strings.HasPrefix(limpo, "+") {
		return limpo
	}
	return "+" + limpo
}

func semMaisNaFrente(phone string) string {
	return strings.TrimPrefix(strings.TrimSpace(phone), "+")
}
