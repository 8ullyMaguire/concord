// Concord Search UI
document.addEventListener('DOMContentLoaded', () => {
  const params = new URLSearchParams(window.location.search);
  const q = params.get('q');
  if (!q) return;
  
  fetch(`/api/v1/search?q=${encodeURIComponent(q)}`)
    .then(r => r.json())
    .then(data => {
      const container = document.querySelector('#search-results');
      if (container && data.results) {
        container.innerHTML = data.results.map(item => 
          `<div class="result-item"><a href="/projects/${item.slug}">${item.name}</a></div>`
        ).join('');
      }
    })
    .catch(console.error);
});
