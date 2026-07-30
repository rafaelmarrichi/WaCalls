package session

import (
	"testing"

	"go.mau.fi/whatsmeow/types"
)

// O alinhamento entre a pergunta e a resposta.
//
// É a única parte desta funcionalidade que decide alguma coisa, e é a que erra
// em silêncio: nada aqui produz erro, produz resposta trocada. Um número marcado
// como "sem WhatsApp" some da campanha sem que ninguém saiba por quê, e um
// marcado como "tem" queima discagem e sobe a taxa de falha do chip.

func resposta(consultado, user string, temWhatsApp bool) types.IsOnWhatsAppResponse {
	return types.IsOnWhatsAppResponse{
		Query: consultado,
		JID:   types.NewJID(user, types.DefaultUserServer),
		IsIn:  temWhatsApp,
	}
}

func TestAlinhaPelaOrdemDaPergunta(t *testing.T) {
	// A Meta responde fora de ordem, e isso é normal: o USync não promete ordem.
	// Alinhar por posição colocaria o resultado de um contato na linha de outro.
	phones := []string{"5514981120008", "5519993858694", "5511987654321"}

	respostas := []types.IsOnWhatsAppResponse{
		resposta("+5511987654321", "5511987654321", true),
		resposta("+5514981120008", "5514981120008", true),
		resposta("+5519993858694", "5519993858694", true),
	}

	resultados := alinharResultados(phones, respostas)

	if len(resultados) != len(phones) {
		t.Fatalf("queria %d resultados, veio %d", len(phones), len(resultados))
	}

	for i, phone := range phones {
		if resultados[i].Phone != phone {
			t.Errorf("posição %d: queria %q, veio %q", i, phone, resultados[i].Phone)
		}
		if !resultados[i].Exists {
			t.Errorf("%s deveria existir", phone)
		}
	}
}

func TestNumeroAusenteDaRespostaNaoSomeNemVazaParaOutraLinha(t *testing.T) {
	// O caso que motiva a função existir. O servidor responde sobre dois dos
	// três, e a resposta simplesmente não menciona o terceiro.
	//
	// Se o consumidor alinhasse por índice, o resultado do terceiro número
	// receberia o dado do segundo, e a lista sairia com um item a menos que a
	// planilha, desalinhando tudo dali para baixo.
	phones := []string{"5514981120008", "5519993858694", "5511900000000"}

	respostas := []types.IsOnWhatsAppResponse{
		resposta("+5514981120008", "5514981120008", true),
		resposta("+5519993858694", "5519993858694", true),
	}

	resultados := alinharResultados(phones, respostas)

	if len(resultados) != 3 {
		t.Fatalf("todo número perguntado precisa de um resultado, veio %d", len(resultados))
	}

	if !resultados[0].Exists || !resultados[1].Exists {
		t.Error("os dois primeiros foram respondidos e deveriam existir")
	}

	if resultados[2].Phone != "5511900000000" {
		t.Errorf("a terceira linha saiu do lugar: %q", resultados[2].Phone)
	}
	if resultados[2].Exists {
		t.Error("número sobre o qual o servidor não respondeu foi dado como existente")
	}
	if resultados[2].JID != "" {
		t.Errorf("número sem WhatsApp veio com JID: %q", resultados[2].JID)
	}
}

func TestRespostaNegativaNaoViraExistente(t *testing.T) {
	phones := []string{"5514981120008"}
	respostas := []types.IsOnWhatsAppResponse{
		resposta("+5514981120008", "5514981120008", false),
	}

	resultados := alinharResultados(phones, respostas)

	if resultados[0].Exists {
		t.Error("IsIn falso foi lido como existente")
	}
	if resultados[0].JID != "" {
		t.Errorf("número sem WhatsApp veio com JID: %q", resultados[0].JID)
	}
}

func TestCasaComOuSemOMaisNaFrente(t *testing.T) {
	// Guardamos o telefone sem `+` e o whatsmeow consulta com. A resposta pode
	// vir de qualquer das duas formas, e as duas precisam casar.
	casos := []struct {
		nome      string
		perguntou string
		respondeu string
	}{
		{"pergunta sem, responde com", "5514981120008", "+5514981120008"},
		{"pergunta com, responde sem", "+5514981120008", "5514981120008"},
		{"os dois sem", "5514981120008", "5514981120008"},
		{"os dois com", "+5514981120008", "+5514981120008"},
	}

	for _, caso := range casos {
		t.Run(caso.nome, func(t *testing.T) {
			resultados := alinharResultados(
				[]string{caso.perguntou},
				[]types.IsOnWhatsAppResponse{resposta(caso.respondeu, "5514981120008", true)},
			)

			if !resultados[0].Exists {
				t.Errorf("não casou: perguntou %q, respondeu %q", caso.perguntou, caso.respondeu)
			}
			// E o número volta como foi perguntado, não como foi respondido.
			if resultados[0].Phone != caso.perguntou {
				t.Errorf("o número mudou na volta: queria %q, veio %q", caso.perguntou, resultados[0].Phone)
			}
		})
	}
}

func TestCasaPeloJidQuandoAConsultaNaoVoltaNaResposta(t *testing.T) {
	// O `Query` pode vir vazio dependendo de como o servidor responde. Nesse
	// caso, o JID é o que resta para casar.
	resultados := alinharResultados(
		[]string{"5514981120008"},
		[]types.IsOnWhatsAppResponse{resposta("", "5514981120008", true)},
	)

	if !resultados[0].Exists {
		t.Error("não casou pelo JID quando a consulta veio vazia na resposta")
	}
}

func TestRespostaVaziaMarcaTodosComoAusentes(t *testing.T) {
	// Servidor que não respondeu sobre ninguém não pode virar lista vazia: o
	// importador precisa de uma linha por contato para saber o que fazer com
	// cada um.
	phones := []string{"5514981120008", "5519993858694"}

	resultados := alinharResultados(phones, nil)

	if len(resultados) != 2 {
		t.Fatalf("queria 2 resultados, veio %d", len(resultados))
	}
	for i, r := range resultados {
		if r.Exists {
			t.Errorf("posição %d marcada como existente sem resposta nenhuma", i)
		}
		if r.Phone != phones[i] {
			t.Errorf("posição %d: queria %q, veio %q", i, phones[i], r.Phone)
		}
	}
}
