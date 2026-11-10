// // @ts-check
// import { defineConfig } from 'astro/config';

// // https://astro.build/config
// export default defineConfig({});


// import { defineConfig } from "astro/config";

// export default defineConfig({
//   vite: {
//     server: {
//       allowedHosts: ["zanele.zelisline.com"],
//     },
//   },
// });



import { defineConfig } from "astro/config";
import node from "@astrojs/node";

export default defineConfig({
  output: "server",

  adapter: node({
    mode: "standalone",
  }),

  vite: {
    server: {
      allowedHosts: ["zanele.zelisline.com"],
    },
  },
});