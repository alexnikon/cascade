/**
 * Small, dependency-free helpers shared by the Vue 2 frontend.
 */

export function updateTransferRates(state, sample, scope, now = performance.now()) {
  const samples = state.transferSamples || (state.transferSamples = Object.create(null));
  const previous = samples[scope];
  const rx = sample.transferRx || 0;
  const tx = sample.transferTx || 0;
  const seconds = previous ? (now - previous.time) / 1000 : 0;

  state.transferRxCurrent = seconds > 0 ? Math.max(0, rx - previous.rx) / seconds : 0;
  state.transferTxCurrent = seconds > 0 ? Math.max(0, tx - previous.tx) / seconds : 0;
  samples[scope] = { rx, tx, time: now };
  sample.transferRxCurrent = state.transferRxCurrent;
  sample.transferTxCurrent = state.transferTxCurrent;
}

export function bytes(bytes, decimals, kib, maxunit) {
  kib = kib || false;
  if (bytes === 0) return '0 B';
  if (Number.isNaN(parseFloat(bytes)) && !Number.isFinite(bytes)) return 'NaN';
  const k = kib ? 1024 : 1000;
  const dm = decimals != null && !Number.isNaN(decimals) && decimals >= 0 ? decimals : 2;
  const sizes = kib
    ? ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB', 'EiB', 'ZiB', 'YiB', 'BiB']
    : ['B', 'KB', 'MB', 'GB', 'TB', 'PB', 'EB', 'ZB', 'YB', 'BB'];
  let i = Math.max(0, Math.floor(Math.log(bytes) / Math.log(k)));
  if (maxunit !== undefined) {
    const index = sizes.indexOf(maxunit);
    if (index !== -1) i = index;
  }
  // eslint-disable-next-line no-restricted-properties
  return `${parseFloat((bytes / Math.pow(k, i)).toFixed(dm))} ${sizes[i]}`;
}

export function formatBitRate(bitsPerSecond) {
  if (!Number.isFinite(bitsPerSecond)) return '—';
  const units = ['b/s', 'Kb/s', 'Mb/s', 'Gb/s', 'Tb/s'];
  let value = Math.max(0, bitsPerSecond);
  let unit = 0;
  while (unit < units.length - 1 && Number(value.toFixed(2)) >= 1000) {
    value /= 1000;
    unit++;
  }
  return `${Number(value.toFixed(2))} ${units[unit]}`;
}

export function sortByProperty(array, property, sort = true) {
  if (sort) {
    return array.sort((a, b) => (typeof a[property] === 'string' ? a[property].localeCompare(b[property]) : a[property] - b[property]));
  }

  return array.sort((a, b) => (typeof a[property] === 'string' ? b[property].localeCompare(a[property]) : b[property] - a[property]));
}

export const commonMethods = {
  dateTime: (value) => {
    return new Intl.DateTimeFormat(undefined, {
      year: 'numeric',
      month: 'short',
      day: 'numeric',
      hour: 'numeric',
      minute: 'numeric',
    }).format(value);
  },
};
