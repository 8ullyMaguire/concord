// Concord Project Detail UI
document.addEventListener('DOMContentLoaded', () => {
  const params = new URLSearchParams(window.location.search);
  const slug = params.get('slug');
  if (!slug) return;
  
  fetch(`/api/v1/projects/${slug}`)
    .then(r => r.json())
    .then(data => {
      const container = document.querySelector('#project-detail');
      if (container) {
        container.innerHTML = `
          <h2>${data.name}</h2>
          <p>${data.description || ''}</p>
          <div class="project-features">
            <h3>Features</h3>
            <ul>${(data.features || []).map(f => `<li>${f.title}</li>`).join('')}</ul>
          </div>
        `;
      }
    })
    .catch(console.error);
});
