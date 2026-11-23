# Active personal branches

Схема — см. `docs/fork-overlay-workflow.md`. `main` строго зеркалит
`upstream/main` (`tbphp/gpt-load`). Runtime-сборка `deploy` всегда
пересоздается из `main` и перечисленных ниже веток; вручную в `deploy` не
коммитим.

| Branch | Type | Purpose | Upstream PR |
|---|---|---|---|
| `local/meta` | local | Этот реестр и fork-overlay workflow | n/a |

## Recipe для пересборки `deploy`

Новая личная ветка добавляется в таблицу выше и в массив `branches` ниже —
массив и есть то, что реально входит в `deploy`.

```bash
git fetch upstream
git checkout main
git merge --ff-only upstream/main

branches=(
  local/meta
)

for branch in "${branches[@]}"; do
  git checkout "$branch"
  git rebase main || break
done

git checkout deploy
git reset --hard main
for branch in "${branches[@]}"; do
  git merge --no-ff "$branch" -m "deploy: include $branch"
done
```

## Когда удалять ветку

- Если upstream реализовал то же самое, ветка удаляется из списка и recipe.
- Если локальное поведение больше не требуется, ветка снимается до следующей
  пересборки `deploy`.
