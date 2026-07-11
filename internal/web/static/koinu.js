(function () {
  var KOINU_PER_DOGE = 100000000;

  function formatDoge(koinu) {
    if (isNaN(koinu) || koinu < 0) return '?';
    var whole = Math.floor(koinu / KOINU_PER_DOGE);
    var frac = koinu % KOINU_PER_DOGE;
    if (frac === 0) return String(whole);
    var fracStr = String(frac).padStart(8, '0').replace(/0+$/, '');
    return whole + '.' + fracStr;
  }

  function wireUp(input) {
    var hint = input.parentElement.querySelector('.doge-hint');
    if (!hint) return;
    input.addEventListener('input', function () {
      var koinu = parseInt(input.value, 10);
      hint.textContent = isNaN(koinu) ? '' : '= ' + formatDoge(koinu) + ' DOGE';
    });
  }

  document.querySelectorAll('.koinu-input').forEach(wireUp);
})();
