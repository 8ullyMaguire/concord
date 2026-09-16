// Concord Kanban Board UI
document.addEventListener('DOMContentLoaded', () => {
  const params = new URLSearchParams(window.location.search);
  const slug = params.get('slug');
  if (!slug) return;
  
  fetch(`/api/v1/projects/${slug}/board`)
    .then(r => r.json())
    .then(data => {
      const container = document.querySelector('#kanban-board');
      if (container) {
        let html = '<div class="kanban">';
        (data.columns || []).forEach(col => {
          html += `<div class="kanban-column"><h3>${col.name}</h3><div class="cards">`;
          const cards = (data.cards || []).filter(c => c.column === col.name);
          cards.forEach(card => {
            html += `<div class="card" draggable="true">${card.feature_title || 'Card'}</div>`;
          });
          html += '</div></div>';
        });
        html += '</div>';
        container.innerHTML = html;
      }
    })
    .catch(console.error);
});
