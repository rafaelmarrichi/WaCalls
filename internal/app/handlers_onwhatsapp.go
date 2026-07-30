package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"wacalls/internal/app/session"
)

// POST /api/sessions/{sid}/onwhatsapp, adicionado pelo fork.
//
// Diz quais dos números têm WhatsApp, para o importador de listas não gastar
// discagem com quem não tem. Ver internal/app/session/onwhatsapp.go para o
// motivo de isso importar para a saúde do chip.

// Teto por requisição.
//
// O `IsOnWhatsApp` é uma consulta USync ao servidor da Meta, e volume chama
// atenção. Cinquenta é o limite do contrato, e serve tanto para conter o tamanho
// de uma consulta quanto para obrigar quem importa a espaçar as chamadas em vez
// de mandar a planilha inteira de uma vez.
const maxNumerosPorConsulta = 50

// Teto do corpo. Cinquenta números formatados não passam de 1 KB; 8 KB é folga
// larga e evita que um corpo enorme seja lido antes de ser recusado.
const maxCorpoOnWhatsApp = 8 << 10

func (s *Server) handleOnWhatsApp(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}

	phones, err := parseOnWhatsAppRequest(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	resultados, err := sess.IsOnWhatsApp(r.Context(), phones)
	if err != nil {
		if errors.Is(err, session.ErrNotPaired) {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": "session is not paired",
			})
			return
		}

		// Falha da consulta ao servidor da Meta. 502 e não 500: o defeito não é
		// nosso, e quem chama precisa distinguir "tente de novo" de "não insista".
		s.log.Error("onwhatsapp query failed", "session", sess.ID(), "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "could not query WhatsApp for these numbers",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"results": resultados})
}

func parseOnWhatsAppRequest(w http.ResponseWriter, r *http.Request) ([]string, error) {
	var req struct {
		Phones []string `json:"phones"`
	}

	corpo := http.MaxBytesReader(w, r.Body, maxCorpoOnWhatsApp)
	if err := json.NewDecoder(corpo).Decode(&req); err != nil {
		return nil, errors.New("invalid JSON body")
	}

	if len(req.Phones) == 0 {
		return nil, errors.New("phones required")
	}

	if len(req.Phones) > maxNumerosPorConsulta {
		return nil, errors.New("at most 50 phones per request")
	}

	// Número vazio ou só espaço vira consulta inútil ao servidor da Meta, e
	// devolver um resultado para ele confundiria quem alinha a resposta com a
	// planilha. Recusa o lote inteiro: silenciar uma linha é pior que recusar.
	for _, phone := range req.Phones {
		if strings.TrimSpace(phone) == "" {
			return nil, errors.New("phones must not contain empty values")
		}
	}

	return req.Phones, nil
}
