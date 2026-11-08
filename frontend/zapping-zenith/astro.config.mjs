// // @ts-check
// import { defineConfig } from 'astro/config';

// // https://astro.build/config
// export default defineConfig({});


import { defineConfig } from "astro/config";

export default defineConfig({
  vite: {
    server: {
      allowedHosts: ["zanele.zelisline.com"],
    },
  },
});