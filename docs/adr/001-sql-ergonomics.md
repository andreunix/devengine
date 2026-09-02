# ADR 001 — SQL Ergonomics: pgx/v5 nativo vs adapter sqlx

**Status:** Aceito — adapter sqlx não será adicionado.  
**Data:** 2026-09-02  
**Autores:** devengine team

---

## Contexto

A issue #11 propunha avaliar um adapter opcional `postgres/sqlx` sobre `pgx/stdlib`
para melhorar a ergonomia de mapeamento de linhas em queries administrativas
(introspection, reports, tooling), sem introduzir uma segunda arquitetura de
acesso a dados no caminho crítico.

## Alternativas analisadas

### Opção A — Adapter `postgres/sqlx` sobre `pgx/stdlib`

**Prós:**
- `Get`, `Select`, `NamedQuery`/`NamedExec` já conhecidos.
- Scan de structs com tags `db:`.

**Contras:**
- Introduz `database/sql` e `pgx/stdlib` no binário, que é exatamente o que a
  issue #2 removeu.
- `sqlx.Tx` e `pgx.Tx` são tipos incompatíveis: um repository que usa `sqlx`
  não pode participar da mesma transação de domínio que um que usa `pgx.Tx`
  sem um boundary explícito e frágil.
- Adiciona uma dependência obrigatória para consumidores que nunca precisarão
  de sqlx.

### Opção B — pgx/v5 nativo com `CollectRows` e `RowToStructByName`

pgx/v5 fornece nativamente:

```go
// Scan genérico em slice de structs
rows, _ := pool.Query(ctx, "SELECT id, name FROM users")
users, _ := pgx.CollectRows(rows, pgx.RowToStructByName[User])

// Named args tipados
pool.Exec(ctx, "INSERT INTO users (name) VALUES (@name)", pgx.NamedArgs{"name": "Alice"})

// Strict named args (detecta parâmetros não usados)
pool.Exec(ctx, sql, pgx.StrictNamedArgs(args))
```

**Prós:**
- Zero dependências extras.
- Funciona com `pgx.Tx` — repositories participam da mesma transação de domínio.
- Sem conversão `database/sql` ↔ pgx.
- `RowToStructByName` cobre 90% dos casos de sqlx.

**Contras:**
- Menos familiar para equipes vindas de `gorm`/`sqlx`.
- Sem `NamedQuery` para queries complexas com muitos parâmetros nomeados.

## Decisão

**Usar pgx/v5 nativo.** Não adicionar adapter sqlx.

Razões:
1. `pgx.CollectRows` + `pgx.RowToStructByName` cobrem os casos de uso listados
   na issue sem fragmentar o boundary transacional.
2. Nenhum dos consumidores reais (Tecno ID, Tecno Ensino, ERP) tem queries onde
   sqlx reduziria complexidade além do que pgx nativo já oferece.
3. A unidade transacional é mais valiosa do que conveniência de scan.

## Consequências

- Issue #11 fechada como `not planned`.
- Adicionar exemplos de `CollectRows`/`RowToStructByName`/`NamedArgs` na
  documentação do package `postgres`.
- Futuros consumidores que quiserem sqlx podem construir um adapter local sem
  afetar a engine.
