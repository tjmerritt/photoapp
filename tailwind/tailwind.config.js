/** @type {import('tailwindcss').Config} */
module.exports = {
  content: [
    './app/**/*.html',
    './app/**/*.js',
  ],
  theme: {
    extend: {
      colors: {
        brand:  '#1a1a2e',
        accent: '#e94560',
        muted:  '#6b7280',
      },
      fontFamily: {
        serif: ['"DM Serif Display"', 'serif'],
        sans:  ['"DM Sans"', 'sans-serif'],
      },
    },
  },
  plugins: [],
};
