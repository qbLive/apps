plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "com.xdreemb52.azhalha"
    compileSdk = 35

    defaultConfig {
        applicationId = "com.xdreemb52.azhalha"
        minSdk = 26
        targetSdk = 35
        versionCode = 8
        versionName = "1.0.7"
        buildConfigField("String", "SITE_URL", "\"https://xdreemb52.vercel.app/\"")
    }

    buildTypes {
        getByName("release") {
            isMinifyEnabled = false
        }
    }

    buildFeatures { buildConfig = true }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions { jvmTarget = "17" }
}

dependencies {
    implementation("androidx.activity:activity-ktx:1.10.1")
    implementation("androidx.core:core-ktx:1.16.0")
    implementation("androidx.webkit:webkit:1.13.0")
}
